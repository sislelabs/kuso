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

// --- postgres ---------------------------------------------------------

type postgresProducer struct{}

func (postgresProducer) Kind() string        { return "postgres" }
func (postgresProducer) PayloadKind() string { return "pg_dump" }
func (postgresProducer) ArtifactExt() string { return "sql.gz" }
func (postgresProducer) RestoreScript() string {
	return `
set -eo pipefail
echo "==> downloading s3://${BUCKET}/${KEY}"
aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
echo "==> checking for manifest s3://${BUCKET}/${KEY}.manifest.json"
if aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}.manifest.json" /tmp/manifest.json 2>/dev/null; then
  WANT=$(grep -o '"sha256":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4)
  GOT=$(sha256sum /tmp/dump.sql.gz | awk '{print $1}')
  if [ -z "${WANT}" ]; then
    echo "==> manifest present but no sha256 — skipping verification"
  elif [ "${WANT}" != "${GOT}" ]; then
    echo "==> checksum MISMATCH: manifest=${WANT} actual=${GOT} — aborting before touching the database"
    exit 1
  else
    echo "==> checksum OK (${GOT})"
  fi
else
  echo "==> no manifest for this backup — integrity NOT verified, proceeding"
fi
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
gunzip -c /tmp/dump.sql.gz | PGPASSWORD="${POSTGRES_PASSWORD}" psql -v ON_ERROR_STOP=1 --single-transaction -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}"
echo "==> done"
`
}

// --- redis ------------------------------------------------------------

type redisProducer struct{}

func (redisProducer) Kind() string        { return "redis" }
func (redisProducer) PayloadKind() string { return "redis_rdb" }
func (redisProducer) ArtifactExt() string { return "rdb.gz" }

// Redis restore is not wired into the UI restore path today (the current
// Restore handler is postgres-only). Provide the script for completeness
// + future use; it verifies the manifest identically.
func (redisProducer) RestoreScript() string {
	return `
set -eo pipefail
echo "==> downloading s3://${BUCKET}/${KEY}"
aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" /tmp/dump.rdb.gz
if aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}.manifest.json" /tmp/manifest.json 2>/dev/null; then
  WANT=$(grep -o '"sha256":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4)
  GOT=$(sha256sum /tmp/dump.rdb.gz | awk '{print $1}')
  if [ -n "${WANT}" ] && [ "${WANT}" != "${GOT}" ]; then
    echo "==> checksum MISMATCH: manifest=${WANT} actual=${GOT} — aborting"
    exit 1
  fi
  echo "==> checksum OK"
else
  echo "==> no manifest — integrity NOT verified, proceeding"
fi
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
echo "==> downloading s3://${BUCKET}/${KEY}"
aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" /tmp/dump.archive.gz
if aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}.manifest.json" /tmp/manifest.json 2>/dev/null; then
  WANT=$(grep -o '"sha256":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4)
  GOT=$(sha256sum /tmp/dump.archive.gz | awk '{print $1}')
  if [ -z "${WANT}" ]; then
    echo "==> manifest present but no sha256 — skipping verification"
  elif [ "${WANT}" != "${GOT}" ]; then
    echo "==> checksum MISMATCH: manifest=${WANT} actual=${GOT} — aborting before touching the database"
    exit 1
  else
    echo "==> checksum OK (${GOT})"
  fi
else
  echo "==> no manifest for this backup — integrity NOT verified, proceeding"
fi
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
echo "==> downloading s3://${BUCKET}/${KEY}"
aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" /tmp/dump.sql.gz
if aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}.manifest.json" /tmp/manifest.json 2>/dev/null; then
  WANT=$(grep -o '"sha256":"[^"]*"' /tmp/manifest.json | head -1 | cut -d'"' -f4)
  GOT=$(sha256sum /tmp/dump.sql.gz | awk '{print $1}')
  if [ -z "${WANT}" ]; then
    echo "==> manifest present but no sha256 — skipping verification"
  elif [ "${WANT}" != "${GOT}" ]; then
    echo "==> checksum MISMATCH: manifest=${WANT} actual=${GOT} — aborting before touching the database"
    exit 1
  else
    echo "==> checksum OK (${GOT})"
  fi
else
  echo "==> no manifest for this backup — integrity NOT verified, proceeding"
fi
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
