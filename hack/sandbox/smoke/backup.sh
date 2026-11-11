# shellcheck shell=bash
# backup: addon backups against the sandbox MinIO. Postgres: write rows,
# back up now, damage the data, restore in place, restore into a sibling.
# Postgres also gets a restore drill and a concurrent-restore refusal.
# MySQL: write rows, back up, damage, restore in place (DB-1/DB-2).
# S3 addon: put an object, snapshot, delete it, restore.

BK_PROJECT="bk"

# As the conn user kuso, like an app: on appRole addons the pod's
# POSTGRES_USER is the postgres superuser, and tables it owned would not
# be dumpable by the backup, which connects as kuso.
bk_psql() {
  local addon="$1" sql="$2"
  node_kubectl -n "$BK_NS" exec "${BK_PROJECT}-${addon}-0" -c postgres -- \
    sh -c 'psql -v ON_ERROR_STOP=1 -U kuso -d "$POSTGRES_DB" -tAc "$1"' _ "$sql"
}
bk_rows() { bk_psql "$1" "SELECT string_agg(name, ',' ORDER BY name) FROM smoke_rows" 2>/dev/null | tr -d '[:space:]'; }
bk_rows_are() { [[ "$(bk_rows "$1")" == "$2" ]]; }
bk_pg_ready() { bk_psql "$1" "SELECT 1" >/dev/null; }

# bk_latest_key <addon> <suffix>: newest backup object key of the addon.
bk_latest_key() {
  "$K" addon-backup list "$BK_PROJECT" "$1" -o json 2>/dev/null \
    | jq -r --arg s "$2" '[.[] | select(.key | endswith($s))] | sort_by(.when) | last | .key // empty'
}
bk_has_new_key() { local k; k="$(bk_latest_key "$1" "$2")"; [[ -n "$k" && "$k" != "$3" ]]; }

# bk_job_done <job>: the Job finished successfully (fails fast on failure).
# bk_job_succeeded <job>: 0 once it succeeded, 2 (stop waiting) when it
# failed or never started, after printing why to stderr.
bk_job_succeeded() {
  local st
  if [[ -z "$1" ]]; then echo "no job was started" >&3; return 2; fi
  st="$(node_kubectl -n "$BK_NS" get job "$1" -o jsonpath='{.status.succeeded}/{.status.failed}' 2>/dev/null)"
  [[ "$st" == 1/* ]] && return 0
  if [[ "${st#*/}" =~ ^[1-9] ]]; then
    node_kubectl -n "$BK_NS" logs "job/$1" --all-containers --tail=20 >&3 2>&1 || true
    return 2
  fi
  return 1
}

# bk_restore <args...>: start a restore and print its job name. The CLI
# may print a pre-restore snapshot line first.
bk_restore() { "$K" addon-backup restore "$@" | awk '/^restore job /{print $3}'; }

bk_mysql() {
  node_kubectl -n "$BK_NS" exec "${BK_PROJECT}-sql-0" -c mysql -- \
    sh -c 'MYSQL_PWD="$MYSQL_PASSWORD" mysql -h 127.0.0.1 -u "$MYSQL_USER" -N -B "$MYSQL_DATABASE" -e "$1"' _ "$1"
}
bk_mysql_ready() { bk_mysql "SELECT 1" >/dev/null 2>&1; }
bk_mysql_rows_are() { [[ "$(bk_mysql "SELECT GROUP_CONCAT(name ORDER BY name) FROM smoke_rows" 2>/dev/null | tr -d '[:space:]')" == "$1" ]]; }

bk_s3_mc() {
  # mc against the addon's MinIO with its root credentials, from inside the pod.
  node_kubectl -n "$BK_NS" exec "${BK_PROJECT}-files-0" -- sh -c \
    'export MC_HOST_l="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@127.0.0.1:9000"; mc "$@"' _ "$@"
}

backup_main() {
  step "project ${BK_PROJECT}: postgres db + db2, s3 files"
  fresh_project "$BK_PROJECT" --repo https://github.com/ivo9999/kuso-demo-todo-api --branch main
  BK_NS="$(project_ns "$BK_PROJECT")"
  "$K" project addon add "$BK_PROJECT" db --kind postgres --version 16 --size small >/dev/null
  "$K" project addon add "$BK_PROJECT" db2 --kind postgres --version 16 --size small >/dev/null
  "$K" project addon add "$BK_PROJECT" files --kind s3 --size small >/dev/null
  "$K" project addon add "$BK_PROJECT" sql --kind mysql --size small >/dev/null
  require "db accepts queries" eventually 300 bk_pg_ready db
  require "db2 accepts queries" eventually 300 bk_pg_ready db2

  step "write rows, back up now"
  bk_psql db "CREATE TABLE smoke_rows (name text); INSERT INTO smoke_rows VALUES ('alpha'), ('beta')" >/dev/null
  "$K" addon-backup schedule "$BK_PROJECT" db --schedule "0 3 * * *" >/dev/null
  local before key job
  before="$(bk_latest_key db .sql.gz)"
  require "backup run started" eventually 120 "$K" addon-backup run "$BK_PROJECT" db
  if eventually 300 bk_has_new_key db .sql.gz "$before"; then
    key="$(bk_latest_key db .sql.gz)"
    pass "dump landed in the sandbox S3 (${key})"
  else
    node_kubectl -n "$BK_NS" get jobs -l "kuso.sislelabs.com/role=backup" >&2 || true
    node_kubectl -n "$BK_NS" logs -l "kuso.sislelabs.com/role=backup" --tail=30 >&2 || true
    fail "dump landed in the sandbox S3"; exit 1
  fi
  check "the stored dump is encrypted (openssl Salted__ header)" \
    grep -q '^Salted__' <<<"$(sandbox_s3 cat "s/kuso-backups/${key}" | head -c 8)"
  check "the manifest records the encryption key id" \
    jq -e '.artifacts[0].encryption.keyId | length > 0' <<<"$(sandbox_s3 cat "s/kuso-backups/${key}.manifest.json")"

  step "damage the data, restore in place"
  bk_psql db "DELETE FROM smoke_rows WHERE name='alpha'; INSERT INTO smoke_rows VALUES ('gamma')" >/dev/null
  job="$(bk_restore "$BK_PROJECT" db "$key" --confirm db)"
  check "a second restore into db is refused while ${job} runs" \
    bash -c '! "$1" addon-backup restore "$2" db "$3" --confirm db --skip-snapshot 2>&1 | grep -q "^restore job "' _ "$K" "$BK_PROJECT" "$key"
  check "in-place restore job ${job} (after its pre-restore snapshot) succeeded" eventually 600 bk_job_succeeded "$job"
  check "rows are back to alpha,beta" eventually 60 bk_rows_are db "alpha,beta"

  step "restore drill of the db dump"
  check "drill restores the dump into a scratch database" \
    bash -c '"$1" addon-backup drill "$2" db --key "$3" | grep -q "restore drill OK"' _ "$K" "$BK_PROJECT" "$key"

  step "restore into the sibling db2"
  job="$(bk_restore "$BK_PROJECT" db "$key" --into db2 --confirm db2)"
  check "restore-into job ${job} succeeded" eventually 300 bk_job_succeeded "$job"
  check "db2 holds alpha,beta" eventually 60 bk_rows_are db2 "alpha,beta"
  check "db is untouched by the sibling restore" bk_rows_are db "alpha,beta"

  step "s3 addon: snapshot and restore an object"
  require "files MinIO is up" eventually 300 bk_s3_mc ls l
  local bucket
  bucket="$(node_kubectl -n "$BK_NS" get secret "${BK_PROJECT}-files-conn" -o jsonpath='{.data.S3_BUCKET}' | base64 -d)"
  bk_s3_mc mb --ignore-existing "l/${bucket}" >/dev/null
  node_kubectl -n "$BK_NS" exec "${BK_PROJECT}-files-0" -- sh -c \
    'echo smoke-object | MC_HOST_l="http://${MINIO_ROOT_USER}:${MINIO_ROOT_PASSWORD}@127.0.0.1:9000" mc pipe "$1"' _ "l/${bucket}/smoke/o.txt" >/dev/null
  "$K" addon-backup schedule "$BK_PROJECT" files --schedule "0 3 * * *" >/dev/null
  before="$(bk_latest_key files "")"
  require "s3 snapshot run started" eventually 120 "$K" addon-backup run "$BK_PROJECT" files
  if eventually 300 bk_has_new_key files "" "$before"; then
    key="$(bk_latest_key files "")"
    pass "s3 snapshot landed (${key})"
    check "snapshot objects are stored encrypted (rclone crypt .bin)" test "${key%.bin}" != "$key"
  else
    node_kubectl -n "$BK_NS" logs -l "kuso.sislelabs.com/role=backup" --tail=30 >&2 || true
    fail "s3 snapshot landed"; exit 1
  fi
  bk_s3_mc rm "l/${bucket}/smoke/o.txt" >/dev/null
  job="$(bk_restore "$BK_PROJECT" files "$key" --confirm files)"
  check "s3 restore job ${job} succeeded" eventually 300 bk_job_succeeded "$job"
  check "the deleted object is back" eventually 60 bk_s3_mc stat "l/${bucket}/smoke/o.txt"

  step "s3 addon: no public TCP port (MinIO is plain HTTP)"
  local out
  if out="$("$K" project addon public-tcp enable "$BK_PROJECT" files 2>&1)"; then
    fail "public TCP on an s3 addon was refused (it was enabled: ${out})"
    "$K" project addon public-tcp disable "$BK_PROJECT" files --yes >/dev/null 2>&1 || true
  elif grep -q 'not configured' <<<"$out"; then
    echo "  (public TCP pool not configured on this sandbox; refusal not exercised)"
  else
    check "public TCP on an s3 addon was refused" grep -q 'plain HTTP' <<<"$out"
  fi

  step "a plain addon delete keeps data; --purge-data clears it afterwards"
  "$K" project addon delete "$BK_PROJECT" db2 -y >/dev/null
  require "db2 addon CR is gone" eventually 120 bk_addon_gone db2
  check "db2 data volume kept" bk_has_pvc db2
  out="$("$K" project addon add "$BK_PROJECT" db2 --kind postgres --version 16 --size small 2>&1 || true)"
  check "re-add is refused and names the purge command" grep -qF "kuso project addon delete ${BK_PROJECT} db2 --purge-data" <<<"$out"
  check "--purge-data after the CR is gone succeeds" "$K" project addon delete "$BK_PROJECT" db2 --purge-data -y
  check "db2 data volume removed" eventually 180 bk_no_pvc db2

  step "mysql addon: back up, damage, restore in place"
  require "sql accepts queries" eventually 300 bk_mysql_ready
  bk_mysql "CREATE TABLE smoke_rows (name VARCHAR(20)); INSERT INTO smoke_rows VALUES ('alpha'), ('beta')" >/dev/null
  "$K" addon-backup schedule "$BK_PROJECT" sql --schedule "0 3 * * *" >/dev/null
  before="$(bk_latest_key sql .sql.gz)"
  require "mysql backup run started" eventually 120 "$K" addon-backup run "$BK_PROJECT" sql
  if eventually 300 bk_has_new_key sql .sql.gz "$before"; then
    key="$(bk_latest_key sql .sql.gz)"
    pass "mysql dump landed (${key})"
  else
    node_kubectl -n "$BK_NS" logs -l "kuso.sislelabs.com/role=backup,kuso.sislelabs.com/addon=${BK_PROJECT}-sql" --tail=30 >&2 || true
    fail "mysql dump landed"; return 0
  fi
  bk_mysql "DELETE FROM smoke_rows WHERE name='alpha'" >/dev/null
  job="$(bk_restore "$BK_PROJECT" sql "$key" --confirm sql --skip-snapshot)"
  check "mysql restore job ${job} succeeded" eventually 300 bk_job_succeeded "$job"
  check "mysql rows are back to alpha,beta" eventually 60 bk_mysql_rows_are "alpha,beta"
}

bk_has_pvc() { [[ -n "$(node_kubectl -n "$BK_NS" get pvc -l "app.kubernetes.io/instance=${BK_PROJECT}-$1" -o name 2>/dev/null)" ]]; }
bk_no_pvc() { ! bk_has_pvc "$1"; }
bk_addon_gone() { ! node_kubectl -n "$BK_NS" get kusoaddon "${BK_PROJECT}-$1" >/dev/null 2>&1; }

backup_cleanup() { delete_project "$BK_PROJECT"; }
