# MySQL Addon Kind + Backup Producer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MySQL as a first-class kuso addon kind (StatefulSet + Service + `-conn` Secret, following the valkey/mongodb pattern), and register a mysql backup producer (mysqldump/mysql restore) in the Piece 3 registry. mongodb is already a kind (Piece 3 shipped its producer), so this plan covers mysql only.

**Architecture:** New `mysql.yaml` chart template (mirrors `mongodb.yaml`), kind added to the validation/HA maps, a `mysqlProducer` in `server-go/internal/backup/registry.go`, a mysql backup CronJob branch, and `mysql-client`/`mariadb-client` in the backup image. Restore is kind-aware already (Piece 3), so mysql plugs in.

**Tech Stack:** Go, Helm, Kubernetes CRD (kind enum), the backup image + registry.

## Global Constraints

- Follow the established 11-step "add a CRD-backed feature" pattern, but scoped: KusoAddon already exists, so this is "add a kind to the existing addon chart", not a new CRD.
- conn Secret keys: `MYSQL_HOST/PORT/USER/PASSWORD/DB` + `MYSQL_URL` + `DATABASE_URL` aliases; `helm.sh/resource-policy: keep` on the Secret (per addon-conn-secret-must-keep-with-pvc). Resources via `kusoaddon.resources` helper (per addon-size-never-maps-to-resources). volumeClaimTemplates MUST stay annotation-free (immutable-VCT rule).
- No HA for mysql in v1 → add `"mysql": true` to `noHAKinds` in `addons.go` AND `unsupported.yaml` `$noHA`.
- Producer registers in the SAME registry as Piece 3; manifest `producer:"mysqldump"`, `payloadKind:"mysqldump"`, artifact ext `sql.gz` (mysqldump | gzip).
- Backup image gains `mysql-client` (alpine: `mysql-client` package = mariadb-client, provides `mysqldump`/`mysql`). Verify installable in alpine:3.21 before committing.
- DB browser (`kuso db sql`), previewdb clone, and mysql HA are OUT of scope (deferred, documented).
- After changes: `go build ./... && go test ./internal/backup/ ./internal/addons/`; `helm template` for the new kind; CRD/kind apply on cluster noted in rollout.

---

### Task 1: mysql producer in the registry

**Files:**
- Modify: `server-go/internal/backup/registry.go` (add `mysqlProducer`, register it)
- Modify: `server-go/internal/backup/registry_test.go` (assert mysql resolves)

**Interfaces:**
- Produces: `mysqlProducer` implementing `Producer` — `Kind()="mysql"`, `PayloadKind()="mysqldump"`, `ArtifactExt()="sql.gz"`, `RestoreScript()` uses `mysql` client with manifest verify.

- [ ] **Step 1: Write the failing test**

```go
// append to server-go/internal/backup/registry_test.go
func TestMysqlProducer(t *testing.T) {
	p, ok := NewDefaultRegistry().For("mysql")
	if !ok {
		t.Fatal("mysql not registered")
	}
	if p.PayloadKind() != "mysqldump" || p.ArtifactExt() != "sql.gz" {
		t.Fatalf("mysql producer metadata wrong: %s/%s", p.PayloadKind(), p.ArtifactExt())
	}
	s := p.RestoreScript()
	for _, want := range []string{"mysql", "MYSQL_URL", "manifest.json", "MISMATCH", "gunzip"} {
		if !strings.Contains(s, want) {
			t.Errorf("mysql restore script missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/backup/ -run TestMysqlProducer -v`
Expected: FAIL — mysql not registered.

- [ ] **Step 3: Add the producer + register**

In `registry.go`, add to the `NewDefaultRegistry` producer slice: `mysqlProducer{},`. Then add:
```go
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
gunzip -c /tmp/dump.sql.gz | mysql "${MYSQL_URL}"
echo "==> done"
`
}
```
Note on `mysql "${MYSQL_URL}"`: the mysql CLI accepts a URI via `--defaults` or host/user flags; simplest robust form is discrete flags. Use instead:
```sh
gunzip -c /tmp/dump.sql.gz | MYSQL_PWD="${MYSQL_PASSWORD}" mysql -h "${MYSQL_HOST}" -u "${MYSQL_USER}" "${MYSQL_DB}"
```
Update the RestoreScript to use the discrete-flag form (and the test's expected substrings to `MYSQL_HOST`/`MYSQL_USER` accordingly — adjust the test list to `{"mysql", "MYSQL_HOST", "manifest.json", "MISMATCH", "gunzip"}`). The conn env for restore (Piece 3's `restoreConnEnv`) must gain a mysql branch — see Task 4.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd server-go && go test ./internal/backup/ -run TestMysqlProducer -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/backup/registry.go server-go/internal/backup/registry_test.go
git commit -m "feat(backup): mysql producer in registry

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: mysql chart template (StatefulSet + Service + conn Secret)

**Files:**
- Create: `operator/helm-charts/kusoaddon/templates/mysql.yaml`

**Interfaces:**
- Produces: for `kind=mysql`, a StatefulSet (mysql:8), a headless/ClusterIP Service named `<release>`, and a `<release>-conn` Secret with `MYSQL_*` + `MYSQL_URL`/`DATABASE_URL`.

- [ ] **Step 1: Author mysql.yaml mirroring mongodb.yaml**

Copy `mongodb.yaml` structure. Key differences:
- Image: `mysql:{{ $version | default "8.0" }}` (root pw via `MYSQL_ROOT_PASSWORD`, db via `MYSQL_DATABASE`, user via `MYSQL_USER`/`MYSQL_PASSWORD`).
- Data mount: `/var/lib/mysql`, `volumeClaimTemplates` name `data` (annotation-free).
- Container env from conn Secret: `MYSQL_ROOT_PASSWORD` (key `MYSQL_PASSWORD`), `MYSQL_DATABASE` (key `MYSQL_DB`), `MYSQL_USER` (`MYSQL_USER`), `MYSQL_PASSWORD` (`MYSQL_PASSWORD`).
- resources: `{{- include "kusoaddon.resources" . | nindent 12 }}`.
- Service port 3306.
- conn Secret (with `helm.sh/resource-policy: keep`), keys:
```yaml
  MYSQL_HOST: {{ $name | b64enc | quote }}
  MYSQL_PORT: {{ "3306" | b64enc | quote }}
  MYSQL_USER: {{ "kuso" | b64enc | quote }}
  MYSQL_PASSWORD: {{ $password | b64enc | quote }}
  MYSQL_DB: {{ $database | b64enc | quote }}
  MYSQL_URL: {{ printf "mysql://kuso:%s@%s:3306/%s" ($password | urlquery) $name $database | b64enc | quote }}
  DATABASE_URL: {{ printf "mysql://kuso:%s@%s:3306/%s" ($password | urlquery) $name $database | b64enc | quote }}
```
- Reuse the `$existing`/`$password` idempotent-password pattern from mongodb.yaml (read existing `MYSQL_PASSWORD` from the surviving conn Secret so a re-add over a kept PVC keeps the same password).
- Guard the whole doc with the same kind gate the other templates use (`{{- if eq .Values.kind "mysql" }}` … `{{- end }}`).
- Add the podSecurityContext/containerSecurityContext includes with the mysql image's non-root UID (mysql:8 runs as uid 999 — set the security context helper accordingly; check how mongodb.yaml passes its UID and mirror with mysql's 999).

- [ ] **Step 2: Render to verify**

Run: `helm template test operator/helm-charts/kusoaddon --set kind=mysql --set project=acme 2>&1 | grep -E 'kind: (StatefulSet|Service|Secret)' | head`
Expected: all three kinds render; exit 0. Also confirm `MYSQL_URL` appears in the rendered Secret.

- [ ] **Step 3: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/mysql.yaml
git commit -m "feat(addon): mysql kind chart template (statefulset/service/conn)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Validation, HA gating, backup CronJob branch, image tooling

**Files:**
- Modify: `server-go/internal/addons/addons.go` (`noHAKinds` + any kind allowlist)
- Modify: `operator/helm-charts/kusoaddon/templates/unsupported.yaml` (`$noHA` list)
- Modify: `operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml` (mysql branch)
- Modify: `build/backup/Dockerfile` (add `mysql-client`)
- Test: `server-go/internal/addons/mysql_test.go`

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/addons/mysql_test.go
package addons

import (
	"context"
	"testing"
)

func TestMysqlAddonRejectsHA(t *testing.T) {
	if !noHAKinds["mysql"] {
		t.Fatal("mysql must be in noHAKinds (no HA template exists)")
	}
	// mysql without HA should be creatable (smoke via Add guard path)
	_ = context.Background()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/addons/ -run TestMysqlAddonRejectsHA -v`
Expected: FAIL — mysql not in noHAKinds.

- [ ] **Step 3: Add mysql to noHAKinds + unsupported.yaml**

`addons.go`: add `"mysql": true,` to `noHAKinds`. `unsupported.yaml`: add `mysql` to the `$noHA` list (match the existing list syntax). If there's an explicit kind allowlist anywhere (grep `"valkey"` in validation), add `mysql` there too.

- [ ] **Step 4: Add the mysql backup CronJob branch**

Mirror the mongodb branch in `backup-cronjob.yaml`. Guard `{{- if and .Values.backup.schedule (eq .Values.kind "mysql") (not .Values.external) (not .Values.useInstanceAddon) }}`. Script (printf manifest, not heredoc):
```yaml
                  TS=$(date -u +%Y%m%dT%H%M%SZ)
                  KEY="${PROJECT}/${ADDON}/${TS}.sql.gz"
                  PREFIX="${PROJECT}/${ADDON}/"
                  echo "==> dumping ${ADDON} → s3://${BUCKET}/${KEY}"
                  MYSQL_PWD="${MYSQL_PASSWORD}" mysqldump -h "${MYSQL_HOST}" -u "${MYSQL_USER}" "${MYSQL_DB}" | gzip > /tmp/dump.sql.gz
                  SHA=$(sha256sum /tmp/dump.sql.gz | awk '{print $1}')
                  BYTES=$(wc -c < /tmp/dump.sql.gz | tr -d ' ')
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/dump.sql.gz "s3://${BUCKET}/${KEY}"
                  echo "==> upload done (sha256=${SHA} bytes=${BYTES})"
                  printf '{"schemaVersion":1,"createdAt":"%s","project":"%s","addon":"%s","addonKind":"mysql","producer":"mysqldump","artifacts":[{"key":"%s","sha256":"%s","bytes":%s,"payloadKind":"mysqldump"}]}\n' \
                    "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "${PROJECT}" "${ADDON}" "${KEY}" "${SHA}" "${BYTES}" > /tmp/manifest.json
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/manifest.json "s3://${BUCKET}/${KEY}.manifest.json"
                  rm -f /tmp/dump.sql.gz /tmp/manifest.json
                  echo "==> manifest done"
```
plus the retention-prune block (copy from redis/mongo branch, kind-agnostic) and the `env:` block with `PROJECT/ADDON/RETENTION_DAYS`, the three `MYSQL_HOST`(value=`kusoaddon.fullname`)/`MYSQL_USER`(value)/`MYSQL_DB`(value) — sourced like the redis branch sources `REDIS_HOST` as a value + `MYSQL_PASSWORD` from `connSecretName` key `MYSQL_PASSWORD` — and the S3 optional block. Mirror the exact CronJob scaffolding from the mongo branch.

- [ ] **Step 5: Add mysql-client to the backup image + verify installable**

`build/backup/Dockerfile` `apk add` list: add `mysql-client`. Verify:
Run: `docker run --rm alpine:3.21 sh -c "apk add --no-cache --simulate mysql-client 2>&1 | tail -3"` (if docker available). Expected: resolves (alpine `mysql-client` = mariadb-client, provides `mysqldump`/`mysql`). If docker unavailable, note that `mysql-client` is a known alpine:3.21 community package and verify at image-build time.

- [ ] **Step 6: Render + build + test**

Run: `helm template test operator/helm-charts/kusoaddon --set kind=mysql --set backup.schedule='0 3 * * *' --set project=acme >/dev/null 2>&1; echo "exit=$?"`; `cd server-go && go build ./... && go test ./internal/addons/ ./internal/backup/`
Expected: render exit 0; build clean; tests PASS.

- [ ] **Step 7: Commit**

```bash
git add server-go/internal/addons/ operator/helm-charts/kusoaddon/ build/backup/Dockerfile
git commit -m "feat(addon): mysql validation/HA gating + backup CronJob + image tooling

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: mysql restore conn env + full pass + docs

**Files:**
- Modify: `server-go/internal/http/handlers/backups.go` (`restoreConnEnv` mysql branch)
- Modify: `docs/superpowers/notes/backup-manifest-rollout.md` (piece 5 note)
- Test: existing `TestRestoreScriptForKind` extended

- [ ] **Step 1: Extend the restore-kind test**

In `backups_restore_script_test.go`'s `TestRestoreScriptForKind`, add:
```go
	my, err := restoreScriptForKind("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(my, "mysql") {
		t.Errorf("mysql restore script wrong")
	}
```

- [ ] **Step 2: Run to verify it fails (mysql conn env missing) / passes for script**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestRestoreScriptForKind -v`
Expected: PASS for the script (producer registered in Task 1) — but the restore Job env would lack `MYSQL_*`. Add the env branch next.

- [ ] **Step 3: Add mysql branch to restoreConnEnv**

In `backups.go` `restoreConnEnv`, add before the postgres default:
```go
	if kind == "mysql" {
		return []corev1.EnvVar{
			envFromSecret("MYSQL_HOST", releaseName+"-conn", "MYSQL_HOST"),
			envFromSecret("MYSQL_USER", releaseName+"-conn", "MYSQL_USER"),
			envFromSecret("MYSQL_DB", releaseName+"-conn", "MYSQL_DB"),
			envFromSecret("MYSQL_PASSWORD", releaseName+"-conn", "MYSQL_PASSWORD"),
		}
	}
```

- [ ] **Step 4: Full build + tests**

Run: `cd server-go && go build ./... && go test ./internal/backup/ ./internal/addons/ ./internal/http/handlers/`
Expected: build clean; all PASS.

- [ ] **Step 5: CLI/web + docs**

- CLI: `kuso get addons` is generic over kind; ensure `kuso project addon add --kind mysql` is accepted (kind validation already updated). Add mysql to any client-side kind list/help. Build the CLI.
- Web: add mysql to the addon-create kind picker + conn-info display (follow how mongodb was added).
- Rollout note: append a Piece 5 section — mysql kind added, mysqldump producer, `mysql-client` in image (needs `make backup-image`), CRD/kind apply on cluster; HA/DB-browser/previewdb deferred.

- [ ] **Step 6: Commit**

```bash
git add server-go/ cli/ web/ docs/
git commit -m "feat(addon): mysql restore env + CLI/web/docs surface

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage** (revised Piece 5 = mysql only, mongo already done in Piece 3):
- mysql as addon kind (chart/validation/conn) → Tasks 2, 3. ✓
- mysql backup producer + CronJob → Tasks 1, 3. ✓
- mysql restore → Tasks 1 (script) + 4 (conn env). ✓
- image tooling (mysql-client) → Task 3. ✓
- keep-policy/resources/VCT rules honored → Task 2. ✓
- HA/DB-browser/previewdb deferred + documented → Task 4 note. ✓

**2. Placeholder scan:** Task 1 Step 3 corrects its own first-draft (`mysql "$MYSQL_URL"`) to the discrete-flag form and tells the implementer to update the test list — resolved inline, not a placeholder. Task 2 describes the template by delta from mongodb.yaml (fully specified keys/ports/paths) rather than pasting 130 lines — acceptable given the exemplar is named and the differing values are all listed. ✓

**3. Type consistency:** `mysqlProducer` methods match the `Producer` interface (Piece 3). `MYSQL_*` conn keys identical across mysql.yaml Secret (Task 2), backup CronJob env (Task 3), restore conn env (Task 4), and the producer's restore script (Task 1). `noHAKinds["mysql"]` consistent between addons.go and unsupported.yaml. ✓
