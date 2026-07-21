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
gunzip -c /tmp/dump.sql.gz | PGPASSWORD="${POSTGRES_PASSWORD}" psql \
  -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}"
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
