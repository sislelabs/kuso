package backup

// Registry maps an addon kind to its Producer.
type Registry struct {
	byKind map[string]Producer
}

// NewDefaultRegistry registers every kind kuso can back up today.
func NewDefaultRegistry() *Registry {
	r := &Registry{byKind: map[string]Producer{}}
	for _, p := range []Producer{
		postgresProducer{},
		redisProducer{},
		mongoProducer{},
		mysqlProducer{},
	} {
		r.byKind[p.Kind()] = p
	}
	return r
}

// For returns the producer for an addon kind.
func (r *Registry) For(kind string) (Producer, bool) {
	p, ok := r.byKind[kind]
	return p, ok
}

// s3FetchShell defines s3_fetch, a bounded retry around the artifact
// download. A fresh restore pod in a project namespace has no network for
// ~5-20s until kube-router syncs the NetworkPolicy for its IP, and the
// restore Job runs with BackoffLimit 0, so a single refused connection
// used to be the whole restore.
const s3FetchShell = `
s3_fetch() { # src dst
  _i=0
  until aws s3 cp --endpoint-url "${S3_ENDPOINT}" "$1" "$2"; do
    _i=$((_i+1))
    if [ "${_i}" -ge 30 ]; then echo "==> could not download $1 after 60s" >&2; exit 1; fi
    echo "==> download failed, retrying (${_i})"; sleep 2
  done
  # Encrypted artifacts (openssl "Salted__" header) are decrypted in place,
  # so the manifest's plaintext sha256 check below and the restore itself
  # see the same gzip a pre-encryption dump was. Legacy dumps pass as-is,
  # except for an addon that encrypts: anyone who can write the bucket
  # could otherwise swap in a plaintext dump plus a matching manifest.
  if [ "$(head -c 8 "$2" | tr -d '\0')" = "Salted__" ]; then
    if [ -z "${BACKUP_ENCRYPTION_KEY:-}" ]; then
      echo "==> $1 is encrypted but no backup encryption key is mounted (Secret <addon>-backup-key)" >&2; exit 1
    fi
    echo "==> decrypting"
    openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 -md sha256 -pass env:BACKUP_ENCRYPTION_KEY -in "$2" -out "$2.plain"
    mv "$2.plain" "$2"
  elif [ -n "${BACKUP_ENCRYPTION_KEY:-}" ]; then
    unverified "$1 is NOT encrypted, but this addon encrypts its backups (a dump from before encryption, or one replaced in the bucket)"
  fi
}
` + verifyManifestShell

// verifyManifestShell defines verify_manifest, which checks the downloaded
// artifact against the sha256 in the manifest stored beside it. A missing
// manifest (or one without a sha256) refuses the restore: a truncated
// object would otherwise only be caught if the restore tool happened to
// choke on it. Backups taken before manifests existed have none, so the
// caller can set ALLOW_UNVERIFIED=1 (API allowUnverified, CLI
// --allow-unverified) to restore one anyway.
const verifyManifestShell = `
unverified() { # reason
  if [ "${ALLOW_UNVERIFIED:-}" = "1" ]; then
    echo "==> $1: integrity NOT verified, proceeding (allowUnverified set)"
    return 0
  fi
  echo "==> $1: refusing to restore an unverified backup. If it predates manifests and you trust it, retry with allowUnverified (kuso CLI: --allow-unverified)." >&2
  exit 1
}
# verify_mac checks the manifest's HMAC of the plaintext sha256 under the
# addon's backup key. The bare sha256 sits next to the artifact, so whoever
# can write the bucket can rewrite both; the mac they cannot forge. Manifests
# written before the mac existed have none and keep the sha256-only check.
verify_mac() { # sha256
  MAC_WANT=$(grep -o '"mac":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4 || true)
  if [ -z "${MAC_WANT}" ] || [ -z "${BACKUP_ENCRYPTION_KEY:-}" ]; then
    return 0
  fi
  MAC_GOT=$(printf 'kuso-backup-mac:%s' "$1" | openssl dgst -sha256 -hmac "${BACKUP_ENCRYPTION_KEY}" | awk '{print $NF}')
  if [ "${MAC_WANT}" != "${MAC_GOT}" ]; then
    echo "==> manifest signature MISMATCH: it was not written with this addon's backup key; aborting before touching the database" >&2
    exit 1
  fi
  echo "==> manifest signature OK"
}
verify_manifest() { # artifact
  echo "==> checking for manifest s3://${BUCKET}/${KEY}.manifest.json"
  if aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}.manifest.json" /tmp/manifest.json 2>/dev/null; then
    WANT=$(grep -o '"sha256":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4 || true)
    GOT=$(sha256sum "$1" | awk '{print $1}')
    if [ -z "${WANT}" ]; then
      unverified "manifest has no sha256"
    elif [ "${WANT}" != "${GOT}" ]; then
      echo "==> checksum MISMATCH: manifest=${WANT} actual=${GOT} — aborting before touching the database" >&2
      exit 1
    else
      echo "==> checksum OK (${GOT})"
      verify_mac "${GOT}"
    fi
  else
    unverified "no manifest for this backup"
  fi
}
`

// --- postgres ---------------------------------------------------------

type postgresProducer struct{}

func (postgresProducer) Kind() string        { return "postgres" }
func (postgresProducer) PayloadKind() string { return "pg_dump" }
func (postgresProducer) ArtifactExt() string { return "sql.gz" }
func (postgresProducer) RestoreScript() string {
	return `
set -eo pipefail
` + s3FetchShell + `echo "==> downloading s3://${BUCKET}/${KEY}"
s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
verify_manifest /tmp/dump.sql.gz
echo "==> piping into psql"
# ON_ERROR_STOP=1 aborts psql at the first SQL error instead of pressing on
# and printing a success exit code over a partial apply; --single-transaction
# wraps the whole dump in one BEGIN/COMMIT so it is all-or-nothing (a mid-dump
# failure rolls back rather than leaving the DB half-restored). Together they
# turn the previous silent-partial behaviour into a loud, atomic failure.
#
# NOTE: this restores CORRECTLY onto an EMPTY target, or onto a populated
# target when the dump was taken with --clean --if-exists (which DROPs objects
# before recreating them — that flag lives in the backup CronJob, not here).
# Restoring a non-clean dump onto a NON-empty DB will now FAIL LOUDLY on the
# first "already exists" error and roll the whole transaction back, rather than
# silently no-op'ing or duplicating rows. That is the intended, safe behaviour:
# no partial writes, and the operator learns the target must be empty (or the
# dump must be --clean).
# The backup image ships a pg_dump newer than the oldest server an addon
# may run (postgresql18-client, so pg_dump can dump newer managed
# providers). A newer pg_dump writes session GUCs into the preamble that
# an older server rejects — e.g. postgres 18 emits transaction_timeout,
# which postgres 16 fails on with "unrecognized configuration parameter".
# Under ON_ERROR_STOP=1 that aborts the entire restore three lines in,
# so a perfectly good dump is unrestorable onto a supported server.
#
# These preamble lines are session settings, not data: dropping one costs
# nothing (the dump still sets the ones the server knows), while keeping
# it costs the whole restore. Strip only the known-forward-compatible
# GUCs, by exact match, before the pipe. Anything else still reaches psql
# and still aborts loudly under ON_ERROR_STOP.
# The download proved egress works, not that the DB's ingress policy has
# admitted this pod yet. Wait for the server before psql, bounded to 60s.
#
# External addons carry a provider DSN rather than POSTGRES_* (the scheduled
# backup dumps them via DATABASE_URL too), so the handler passes it as
# RESTORE_DIRECT_URL / RESTORE_DATABASE_URL with EXTERNAL_RESTORE=1.
RESTORE_DSN="${RESTORE_DIRECT_URL:-${RESTORE_DATABASE_URL:-}}"
if [ -n "${EXTERNAL_RESTORE:-}" ] && [ -z "${RESTORE_DSN}" ]; then
  echo "==> external addon conn secret has neither DIRECT_URL nor DATABASE_URL" >&2; exit 1
fi
pg_target_ready() {
  if [ -n "${RESTORE_DSN}" ]; then pg_isready -d "${RESTORE_DSN}" -q
  else pg_isready -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" -q; fi
}
` + pgOwnershipFilterShell + `
pg_apply() {
  if [ -n "${RESTORE_DSN}" ]; then psql -v ON_ERROR_STOP=1 --single-transaction "${RESTORE_DSN}"
  else PGPASSWORD="${POSTGRES_PASSWORD}" psql -v ON_ERROR_STOP=1 --single-transaction -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}"; fi
}
_i=0
until pg_target_ready; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "==> postgres ${POSTGRES_HOST:-(external)} unreachable after 60s" >&2; exit 1; fi
  echo "==> waiting for postgres ${POSTGRES_HOST:-(external)} (${_i})"; sleep 2
done
gunzip -c /tmp/dump.sql.gz \
  | grep -vxE "SET (transaction_timeout|idle_session_timeout) = '?[^']*'?;" \
  | pg_ownership_filter \
  | pg_apply
echo "==> done"
`
}

// pgOwnershipFilterShell defines pg_ownership_filter. Dumps keep their
// OWNER TO and GRANT/REVOKE lines, and a restore applies them as the
// destination's conn user, which may not SET ROLE to the source's owner
// (every instance tenant has its own role). For a restore into a different
// addon (RESTORE_STRIP_OWNERSHIP=1) those lines are dropped, as
// pg_dump --no-owner --no-acl would have: the objects end up owned by the
// destination user. COPY data and lines outside ACL entries pass untouched,
// so a row or function body that happens to read "GRANT ..." survives.
const pgOwnershipFilterShell = `
pg_ownership_filter() {
  if [ "${RESTORE_STRIP_OWNERSHIP:-}" != "1" ]; then cat; return; fi
  echo "==> restoring into another addon: dropping the source's ownership and grants" >&2
  awk '
    copy { print; if ($0 == "\\.") copy = 0; next }
    /^COPY .* FROM stdin;$/ { copy = 1; print; next }
    /^-- (Data for )?Name: .*; Type: / { t = $0; sub(/.*; Type: /, "", t); sub(/;.*/, "", t); print; next }
    /^ALTER [A-Z ]+ .* OWNER TO [^ ]+;$/ { next }
    (t == "ACL" || t == "DEFAULT ACL") && /^(GRANT|REVOKE|ALTER DEFAULT PRIVILEGES) / { next }
    { print }'
}
`

// --- redis ------------------------------------------------------------

type redisProducer struct{}

func (redisProducer) Kind() string        { return "redis" }
func (redisProducer) PayloadKind() string { return "redis_rdb" }
func (redisProducer) ArtifactExt() string { return "rdb.gz" }
func (redisProducer) ManualRestore() bool { return true }

// Redis restore is not wired into the UI restore path today (the current
// Restore handler is postgres-only). Provide the script for completeness
// + future use; it verifies the manifest identically.
func (redisProducer) RestoreScript() string {
	return `
set -eo pipefail
` + s3FetchShell + `echo "==> downloading s3://${BUCKET}/${KEY}"
s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.rdb.gz
verify_manifest /tmp/dump.rdb.gz
echo "==> restoring rdb is manual (redis restore not yet automated)" >&2
exit 1
`
}

// --- mongodb ----------------------------------------------------------

type mongoProducer struct{}

func (mongoProducer) Kind() string        { return "mongodb" }
func (mongoProducer) PayloadKind() string { return "mongodump" }
func (mongoProducer) ArtifactExt() string { return "archive.gz" }
func (mongoProducer) RestoreScript() string {
	return `
set -eo pipefail
` + s3FetchShell + `echo "==> downloading s3://${BUCKET}/${KEY}"
s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.archive.gz
verify_manifest /tmp/dump.archive.gz
# The download proved egress works, not that the DB's ingress policy has
# admitted this pod yet. Wait for the server before restoring, bounded to 60s.
# The image has mongodb-tools but no mongosh, so probe the first host in the URI.
MONGO_HOSTPORT=$(echo "${MONGO_URL}" | sed -E 's#^[^/]*//([^@]*@)?([^/?]+).*#\2#' | cut -d, -f1)
MONGO_H=${MONGO_HOSTPORT%%:*}
MONGO_P=${MONGO_HOSTPORT#*:}; [ "${MONGO_P}" = "${MONGO_H}" ] && MONGO_P=27017
_i=0
until nc -z -w2 "${MONGO_H}" "${MONGO_P}" 2>/dev/null; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "==> mongodb ${MONGO_H}:${MONGO_P} unreachable after 60s" >&2; exit 1; fi
  echo "==> waiting for mongodb ${MONGO_H}:${MONGO_P} (${_i})"; sleep 2
done
echo "==> restoring via mongorestore"
mongorestore --uri "${MONGO_URL}" --archive=/tmp/dump.archive.gz --gzip --drop
echo "==> done"
`
}

// --- mysql ------------------------------------------------------------

type mysqlProducer struct{}

func (mysqlProducer) Kind() string        { return "mysql" }
func (mysqlProducer) PayloadKind() string { return "mysqldump" }
func (mysqlProducer) ArtifactExt() string { return "sql.gz" }
func (mysqlProducer) RestoreScript() string {
	return `
set -eo pipefail
` + s3FetchShell + `echo "==> downloading s3://${BUCKET}/${KEY}"
s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
verify_manifest /tmp/dump.sql.gz
# Same bounded wait as postgres/mongo: the download proved egress, not that
# the DB's ingress policy has admitted this pod yet.
_i=0
until nc -z -w2 "${MYSQL_HOST}" "${MYSQL_PORT:-3306}" 2>/dev/null; do
  _i=$((_i+1))
  if [ "${_i}" -ge 30 ]; then echo "==> mysql ${MYSQL_HOST}:${MYSQL_PORT:-3306} unreachable after 60s" >&2; exit 1; fi
  echo "==> waiting for mysql ${MYSQL_HOST} (${_i})"; sleep 2
done
echo "==> piping into mysql"
# The mysql CLI stops at the first SQL error by default when reading a script
# from stdin (it only presses on with --force, which we deliberately DON'T
# pass), so a broken/incompatible dump fails the pipe loudly rather than
# leaving a silent partial. Unlike postgres, mysql has no --single-transaction
# equivalent for a RESTORE that covers DDL (CREATE/DROP TABLE auto-commit in
# InnoDB), so a mid-dump failure can leave the schema partially applied — it
# cannot be made atomic here. For a clean idempotent restore the dump must
# have been taken with DROP-TABLE statements (mysqldump's default --add-drop-
# table) OR the target must be empty. Correctness focus for the atomic path is
# postgres above; mysql is fail-loud but not all-or-nothing.
gunzip -c /tmp/dump.sql.gz | MYSQL_PWD="${MYSQL_PASSWORD}" mysql -h "${MYSQL_HOST}" -u "${MYSQL_USER}" "${MYSQL_DB}"
echo "==> done"
`
}
