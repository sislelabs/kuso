# Backup Producer Registry + mongodb Producer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce a Go producer registry (`server-go/internal/backup`) that maps an addon kind to its backup/restore shell, extract postgres/redis into it, ship a **mongodb** producer end-to-end (backup CronJob branch + kind-aware restore + `mongodb-tools` in the image), and fix the pre-existing missing-redis-cli image gap.

**Architecture:** A `Producer` interface returns the shell fragments a Job runs (it does not talk to the DB itself — the Job pod does, against the addon's `-conn` Secret). A `Registry` resolves `For(kind)`. The restore handler (`backups.go`) becomes kind-aware via the registry; the backup CronJob (`backup-cronjob.yaml`) gains a mongodb branch mirroring the pg/redis branches. Postgres restore keeps byte-for-byte today's behavior (validated by an unchanged-script test).

**Tech Stack:** Go, Helm (sh in CronJob), the `kuso-backup` alpine image (add `mongodb-tools` + `redis`).

## Global Constraints

- Manifest format is fixed by Piece 2 (`server-go/internal/backup/manifest.go`): `{schemaVersion,createdAt,project,addon,addonKind,producer,artifacts:[{key,sha256,bytes,payloadKind}]}`. New producers emit the SAME shape.
- Backup scripts run inside a YAML block scalar → **use `printf`, never a heredoc** (an indented heredoc terminator is never matched; Piece 2 hit this).
- mongodb `-conn` Secret (from `mongodb.yaml`) exposes `MONGO_URL` (full `mongodb://…?authSource=admin` URI) plus `MONGODB_URI`/`DATABASE_URL` aliases and `MONGO_HOST/PORT/USER/PASSWORD/DB`. Use `MONGO_URL` for `mongodump --uri`.
- The addon `-conn` Secret name = `include "kusoaddon.connSecretName"` = `<release>-conn`.
- Restore Job env sources connection params from `<release>-conn` and S3 creds from `kuso-backup-s3` (both already wired in `backups.go`).
- postgres restore behavior MUST NOT change (it's the incident-critical path). The registry returns the exact current script for postgres.
- After server-go changes: `cd server-go && go build ./...` + `go test ./internal/backup/ ./internal/http/handlers/` must pass. Chart changes verified with `helm template`.
- The image change (`build/backup/Dockerfile`) requires a `make backup-image` rebuild+push to take effect on the cluster — note in the rollout, don't attempt to push from here.

---

### Task 1: Producer interface + Registry + postgres/redis producers

**Files:**
- Create: `server-go/internal/backup/producer.go`
- Create: `server-go/internal/backup/registry.go`
- Test: `server-go/internal/backup/registry_test.go`

**Interfaces:**
- Produces:
  - `type Producer interface { Kind() string; PayloadKind() string; ArtifactExt() string; RestoreScript() string }`
    - `Kind()` — addon kind, e.g. `"postgres"`, `"redis"`, `"mongodb"`.
    - `PayloadKind()` — manifest payloadKind, e.g. `"pg_dump"`, `"redis_rdb"`, `"mongodump"`.
    - `ArtifactExt()` — artifact suffix incl. leading dot-less ext used in the key, e.g. `"sql.gz"`, `"rdb.gz"`, `"archive.gz"`.
    - `RestoreScript()` — the full restore shell (download + verify manifest + apply). Backup shell stays in the chart for now (CronJob), so no `BackupScript()` on the interface yet — only restore is Go-driven. (Documented deviation from spec's 2-method interface: backup shell lives in helm; keeping it there avoids a bigger chart refactor and matches where it already is.)
  - `type Registry struct { ... }` with `func NewDefaultRegistry() *Registry` (registers postgres, redis, mongodb) and `func (r *Registry) For(kind string) (Producer, bool)`.
  - Concrete: `postgresProducer`, `redisProducer`, `mongoProducer` (unexported structs implementing Producer).

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/backup/registry_test.go
package backup

import (
	"strings"
	"testing"
)

func TestRegistryResolvesKnownKinds(t *testing.T) {
	r := NewDefaultRegistry()
	for kind, wantPayload := range map[string]string{
		"postgres": "pg_dump",
		"redis":    "redis_rdb",
		"mongodb":  "mongodump",
	} {
		p, ok := r.For(kind)
		if !ok {
			t.Fatalf("For(%q) not found", kind)
		}
		if p.PayloadKind() != wantPayload {
			t.Errorf("For(%q).PayloadKind() = %q, want %q", kind, p.PayloadKind(), wantPayload)
		}
	}
}

func TestRegistryUnknownKind(t *testing.T) {
	r := NewDefaultRegistry()
	if _, ok := r.For("nats"); ok {
		t.Error("nats should not be backable yet")
	}
}

func TestPostgresRestoreScriptUnchangedContract(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	for _, want := range []string{"gunzip -c /tmp/dump.sql.gz", "psql", "manifest.json", "sha256sum", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("postgres restore script missing %q", want)
		}
	}
}

func TestMongoRestoreScript(t *testing.T) {
	p, _ := NewDefaultRegistry().For("mongodb")
	s := p.RestoreScript()
	for _, want := range []string{"mongorestore", "--archive", "--gzip", "MONGO_URL", "manifest.json", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("mongo restore script missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/backup/ -run 'TestRegistry|TestPostgresRestore|TestMongoRestore' -v`
Expected: FAIL — undefined `NewDefaultRegistry` etc.

- [ ] **Step 3: Write the producer interface + registry**

```go
// server-go/internal/backup/producer.go

package backup

// Producer emits the restore shell for one addon kind and the metadata a
// backup records. Backup shell currently lives in the helm CronJob
// (backup-cronjob.yaml); this interface drives the Go-built restore Job
// and lets the handler ask "is this kind backable?".
type Producer interface {
	Kind() string        // addon kind, e.g. "mongodb"
	PayloadKind() string // manifest payloadKind, e.g. "mongodump"
	ArtifactExt() string // artifact key suffix, e.g. "archive.gz"
	RestoreScript() string
}
```

```go
// server-go/internal/backup/registry.go

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
```

Note: the mongo producer is added in Task 2 (its own file) so this task fails to compile with `mongoProducer{}` until Task 2 — to keep Task 1 self-contained and green, ALSO create a minimal `mongoProducer` stub here in `registry.go` and flesh it out in Task 2. Add at the bottom of `registry.go`:
```go
// --- mongodb (filled in Task 2) ---------------------------------------

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
```
(This makes Task 1 fully green; Task 2 wires the CronJob backup branch + image tooling + handler use.)

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server-go && go test ./internal/backup/ -run 'TestRegistry|TestPostgresRestore|TestMongoRestore' -v`
Expected: PASS (all four).

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/backup/producer.go server-go/internal/backup/registry.go server-go/internal/backup/registry_test.go
git commit -m "feat(backup): producer registry (postgres/redis/mongodb restore)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: mongodb backup CronJob branch + image tooling

**Files:**
- Modify: `operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml` (add a mongodb branch)
- Modify: `build/backup/Dockerfile` (add `mongodb-tools` + `redis`)

**Interfaces:**
- Produces: scheduled mongodb backups writing `s3://…/<TS>.archive.gz` + `<TS>.archive.gz.manifest.json` (payloadKind `mongodump`).

- [ ] **Step 1: Add mongodb-tools + redis to the backup image**

In `build/backup/Dockerfile`, change the `apk add` block to:
```dockerfile
RUN apk add --no-cache \
      bash \
      ca-certificates \
      aws-cli \
      postgresql16-client \
      redis \
      mongodb-tools \
      gzip
```
(Comment the two additions: `redis` provides `redis-cli` — the redis CronJob needed it and it was missing; `mongodb-tools` provides `mongodump`/`mongorestore`.)

- [ ] **Step 2: Add the mongodb backup branch to the CronJob**

Append a new document to `backup-cronjob.yaml` (after the redis branch, before/around the s3 branch), mirroring the redis branch structure. The guard:
```yaml
{{- if and .Values.backup.schedule (eq .Values.kind "mongodb") (not .Values.external) (not .Values.useInstanceAddon) }}
```
The container `args` script (printf manifest, NOT heredoc):
```yaml
                - |
                  set -eu
                  if [ -z "${BUCKET:-}" ]; then
                    echo "==> backup S3 not configured (no kuso-backup-s3 secret) — skipping"
                    exit 0
                  fi
                  TS=$(date -u +%Y%m%dT%H%M%SZ)
                  KEY="${PROJECT}/${ADDON}/${TS}.archive.gz"
                  PREFIX="${PROJECT}/${ADDON}/"
                  echo "==> dumping ${ADDON} → s3://${BUCKET}/${KEY}"
                  mongodump --uri "${MONGO_URL}" --archive --gzip > /tmp/dump.archive.gz
                  SHA=$(sha256sum /tmp/dump.archive.gz | awk '{print $1}')
                  BYTES=$(wc -c < /tmp/dump.archive.gz | tr -d ' ')
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/dump.archive.gz "s3://${BUCKET}/${KEY}"
                  echo "==> upload done (sha256=${SHA} bytes=${BYTES})"
                  printf '{"schemaVersion":1,"createdAt":"%s","project":"%s","addon":"%s","addonKind":"mongodb","producer":"mongodump","artifacts":[{"key":"%s","sha256":"%s","bytes":%s,"payloadKind":"mongodump"}]}\n' \
                    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PROJECT}" "${ADDON}" "${KEY}" "${SHA}" "${BYTES}" > /tmp/manifest.json
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/manifest.json "s3://${BUCKET}/${KEY}.manifest.json"
                  rm -f /tmp/dump.archive.gz /tmp/manifest.json
                  echo "==> manifest done"
```
The `env:` block mirrors the redis/postgres branches but with the mongo conn key. Include (copying the postgres branch's PROJECT/ADDON/RETENTION + S3 block, replacing the DB creds):
```yaml
              env:
                - { name: PROJECT, value: {{ .Values.project | default "" | quote }} }
                - { name: ADDON,   value: {{ .Release.Name | quote }} }
                - { name: RETENTION_DAYS, value: {{ .Values.backup.retentionDays | default 14 | quote }} }
                - name: MONGO_URL
                  valueFrom:
                    secretKeyRef:
                      name: {{ include "kusoaddon.connSecretName" . }}
                      key: MONGO_URL
                - { name: BUCKET,        valueFrom: { secretKeyRef: { name: kuso-backup-s3, key: bucket, optional: true } } }
                - { name: S3_ENDPOINT,   valueFrom: { secretKeyRef: { name: kuso-backup-s3, key: endpoint, optional: true } } }
                - { name: AWS_ACCESS_KEY_ID,     valueFrom: { secretKeyRef: { name: kuso-backup-s3, key: accessKeyId, optional: true } } }
                - { name: AWS_SECRET_ACCESS_KEY, valueFrom: { secretKeyRef: { name: kuso-backup-s3, key: secretAccessKey, optional: true } } }
                - { name: AWS_DEFAULT_REGION,    valueFrom: { secretKeyRef: { name: kuso-backup-s3, key: region }, optional: true } }
```
Include the retention-prune block copied from the redis branch (identical logic; it's kind-agnostic — operates on `${PREFIX}`). Close with `{{- end }}`.

Copy the exact CronJob metadata/spec scaffolding (apiVersion/kind/metadata/labels/schedule/jobTemplate/podSecurityContext/containerSecurityContext/image) from the redis branch so structure + security context match. Use the same `image` default `ghcr.io/sislelabs/kuso-backup:latest`.

- [ ] **Step 3: Render to verify**

Run: `helm template test operator/helm-charts/kusoaddon --set kind=mongodb --set backup.schedule='0 3 * * *' --set project=acme 2>&1 | grep -c 'mongodump'`
Expected: ≥1 (the dump command present); overall render exit 0. Also render `--set kind=postgres` and `--set kind=redis` to confirm the existing branches still render (no accidental brace mismatch).

- [ ] **Step 4: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml build/backup/Dockerfile
git commit -m "feat(backup): mongodb backup CronJob branch + mongodb-tools/redis in image

Adds mongodump-based scheduled backup with sha256 manifest. Also adds
redis-cli to the backup image (the redis branch needed it and it was
missing).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Make the restore handler kind-aware via the registry

**Files:**
- Modify: `server-go/internal/http/handlers/backups.go` (restore Job: pick script by addon kind)
- Modify: `server-go/internal/http/handlers/backups_restore_script_test.go` (adjust for the registry-driven script)

**Interfaces:**
- Consumes: `backup.NewDefaultRegistry().For(kind)` (Task 1).
- Produces: restore Job uses the producer's `RestoreScript()` for the destination addon's kind; unknown/unbackable kind → 400 with a clear message; postgres path byte-identical to today.

- [ ] **Step 1: Write/adjust the failing test**

The existing `restoreScript()` free function is replaced by registry lookup. Update the test to assert the handler resolves postgres from the registry (keep a focused unit on the registry, already in Task 1) and add a handler-level guard test. Replace `TestRestoreScriptVerifiesChecksum` body with a registry-based check and add an unbackable-kind expectation via a small helper `restoreScriptForKind(kind string) (string, error)`:
```go
func TestRestoreScriptForKind(t *testing.T) {
	s, err := restoreScriptForKind("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "psql") || !strings.Contains(s, "MISMATCH") {
		t.Errorf("postgres restore script wrong")
	}
	if _, err := restoreScriptForKind("nats"); err == nil {
		t.Error("nats should be rejected as not restorable")
	}
}
```
(Delete the now-obsolete `TestRestoreScriptVerifiesChecksum` that referenced the removed free function; `TestIsManifestKey` stays.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestRestoreScriptForKind -v`
Expected: FAIL — `restoreScriptForKind` undefined.

- [ ] **Step 3: Implement `restoreScriptForKind` + use it in Restore**

Remove the `restoreScript()` free function added in Piece 2. Add:
```go
// restoreScriptForKind returns the restore shell for an addon kind via
// the producer registry. Unknown/unbackable kinds are rejected so the
// caller can return a clear 400 instead of minting a doomed Job.
func restoreScriptForKind(kind string) (string, error) {
	p, ok := backup.NewDefaultRegistry().For(kind)
	if !ok {
		return "", fmt.Errorf("addon kind %q is not restorable", kind)
	}
	return p.RestoreScript(), nil
}
```
Add the import `"kuso/server/internal/backup"` (confirm module path prefix by checking an existing import in the file — it is `kuso/server/internal/...`).

In `Restore`, after resolving `srcCR` (which carries `.Spec.Kind`), compute the script and guard:
```go
	script, err := restoreScriptForKind(srcCR.Spec.Kind)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
```
Change the Job container `Args` from `[]string{restoreScript()}` to `[]string{script}`.

Note the mongo restore needs `MONGO_URL` in the Job env. The current restore Job env is postgres-specific (`POSTGRES_*` from `-conn`). For THIS task keep the postgres env block; add a kind-aware env note: if `srcCR.Spec.Kind == "mongodb"`, the Job must instead source `MONGO_URL` from `<release>-conn`. Implement the minimal branch:
```go
	connEnv := postgresConnEnv(releaseName) // existing POSTGRES_* refs, extract to a helper
	if srcCR.Spec.Kind == "mongodb" {
		connEnv = []corev1.EnvVar{envFromSecret("MONGO_URL", releaseName+"-conn", "MONGO_URL")}
	}
```
Then build the Job's `Env` as `append(connEnv, <the S3 creds envs>...)`. Extract the existing inline `POSTGRES_*` + S3 env list into `postgresConnEnv` / a shared S3 env slice so both kinds share the S3 block. Keep the postgres list byte-identical.

- [ ] **Step 4: Run tests + build**

Run: `cd server-go && go test ./internal/http/handlers/ -run 'TestRestoreScriptForKind|TestIsManifestKey' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/http/handlers/backups.go server-go/internal/http/handlers/backups_restore_script_test.go
git commit -m "feat(backup): kind-aware restore via producer registry

Restore resolves the script + conn env by the addon's kind (postgres
unchanged; mongodb via mongorestore). Unbackable kinds -> 400.

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Full build/test + rollout note update

**Files:**
- Modify: `docs/superpowers/notes/backup-manifest-rollout.md` (add mongodb + image-rebuild note)

- [ ] **Step 1: Full build + backup tests**

Run: `cd server-go && go build ./... && go test ./internal/backup/ ./internal/http/handlers/ 2>&1 | tail -10`
Expected: build clean; both packages PASS (note any pre-existing unrelated failures).

- [ ] **Step 2: Update the rollout note**

Append to `docs/superpowers/notes/backup-manifest-rollout.md`:
```markdown

## Piece 3 addendum — producer registry + mongodb

- New `server-go/internal/backup` registry drives the restore Job's script
  per addon kind (postgres/redis/mongodb registered; others → "not
  restorable" 400).
- mongodb now has a scheduled backup CronJob branch (mongodump --archive
  --gzip) + sha256 manifest, and restore via mongorestore --drop.
- The `kuso-backup` image gained `mongodb-tools` (mongodump/mongorestore)
  AND `redis` (redis-cli — the redis branch was already broken without it).
  This requires `make backup-image` to rebuild+push before mongodb/redis
  backups run on the cluster — the auto-updater does NOT rebuild this image.
- Other addon kinds (valkey/clickhouse/rabbitmq/meilisearch/nats/redpanda)
  are registry-ready but not yet implemented — follow-up work.
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/notes/backup-manifest-rollout.md
git commit -m "docs(backup): piece 3 rollout note (registry + mongodb)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage** (revised Piece 3):
- Registry keyed by kind, postgres/redis extracted → Task 1. ✓
- mongodb producer (mongodump/mongorestore) end-to-end → Task 1 (restore) + Task 2 (backup CronJob + image). ✓
- `mongodb-tools` in image → Task 2. ✓
- Fix missing redis-cli → Task 2. ✓
- Restore kind-aware, postgres unchanged, unknown kind rejected → Task 3. ✓
- No volume producer (deferred, documented in spec revision) → not implemented, by design. ✓

**2. Placeholder scan:** No TBD/TODO. Every code step shows full code. Task 3 Step 3 says "extract to a helper" and shows the helper + the branch — the implementer copies the existing postgres env list verbatim into `postgresConnEnv` (its current content is visible in backups.go ~457-467). Acceptable — the source is a byte-for-byte move, and the plan names it explicitly. ✓

**3. Type consistency:** `Producer` methods (`Kind`,`PayloadKind`,`ArtifactExt`,`RestoreScript`) consistent across producer.go, the three structs, and the tests. `NewDefaultRegistry`/`For` consistent between registry.go, registry_test.go, and Task 3's `restoreScriptForKind`. Manifest JSON emitted by Task 2's mongo CronJob (`payloadKind":"mongodump"`) matches `mongoProducer.PayloadKind()` and the Piece 2 `Manifest` struct. `MONGO_URL` key consistent between mongodb.yaml conn secret, Task 2 CronJob env, Task 2 dump command, and Task 3 restore env. ✓

Deviation logged: `Producer` has no `BackupScript()` (spec showed one). Backup shell stays in the helm CronJob where it already lives; only restore is Go-driven this piece. This avoids a full chart→Go backup-script migration that would balloon scope. Recorded in Task 1 interfaces + spec revision.
