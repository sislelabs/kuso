# Backup Manifest + sha256 Verify Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every backup writes a `manifest.json` (per-artifact sha256, size, kind) alongside its S3 artifact, and every restore verifies the artifact's sha256 against the manifest before applying — aborting on mismatch, warning (not failing) when a pre-manifest backup has no manifest.

**Architecture:** The backup CronJob scripts (postgres + redis, in `kusoaddon/templates/backup-cronjob.yaml`) change from a streaming `dump | gzip | aws s3 cp -` to a temp-file flow: dump → gzip to `/tmp` → `sha256sum` → upload artifact → write+upload a sibling `<artifact>.manifest.json`. The restore Job (built in Go in `backups.go`) downloads the manifest next to the artifact, verifies the checksum, then applies as before. A new Go package `server-go/internal/backup` holds the manifest type shared by handler and (later pieces) the producer registry.

**Tech Stack:** Go, Helm (sh scripts in CronJob), the `kuso-backup` container image (needs `sha256sum` — coreutils; already present in the image, confirmed by Task 1's precondition check).

## Global Constraints

- Backup artifacts live at `s3://<bucket>/<project>/<addonFQN>/<TS>.<ext>` where ext is `sql.gz` (postgres) or `rdb.gz` (redis); `addonFQN` = helm release name = `<project>-<short>`. The manifest for an artifact `<X>` is stored at `<X>.manifest.json` (same prefix).
- Manifest is JSON, schemaVersion 1, and records NO secret values (mirrors the existing "secrets stay in encrypted columns" rule).
- Restore must stay backward-compatible: a missing manifest → warn + proceed (integrity unverified), never fail.
- The `kuso-backup` image is `ghcr.io/sislelabs/kuso-backup:latest`. Script changes ship in the helm chart (backup side) and in `backups.go` (restore side) — no server-go release is needed for the chart, but the chart must be re-applied to the cluster (`kubectl apply` of the rendered addon chart via the operator, or a helm upgrade path — see Task 5).
- After server-go changes: `cd server-go && go build ./...` must pass. After CLI-surfacing changes (none in this piece) rebuild the CLI.
- Do NOT change the S3 key scheme or the List prefix — the manifest is an ADDITIONAL object, and List must not start returning `.manifest.json` objects as if they were backups (Task 4 filters them out).

---

### Task 1: Manifest type + package (Go)

**Files:**
- Create: `server-go/internal/backup/manifest.go`
- Test: `server-go/internal/backup/manifest_test.go`

**Interfaces:**
- Produces:
  - `type Artifact struct { Key string; SHA256 string; Bytes int64; PayloadKind string }` (JSON tags `key`,`sha256`,`bytes`,`payloadKind`)
  - `type Manifest struct { SchemaVersion int; CreatedAt string; Project string; Addon string; AddonKind string; Producer string; Artifacts []Artifact }` (JSON tags `schemaVersion`,`createdAt`,`project`,`addon`,`addonKind`,`producer`,`artifacts`)
  - `const SchemaVersion = 1`
  - `func ManifestKey(artifactKey string) string` → `artifactKey + ".manifest.json"`
  - `func Parse(b []byte) (*Manifest, error)` — unmarshal + reject unknown schemaVersion (> SchemaVersion) with a clear error.
  - `func (m *Manifest) ArtifactFor(key string) (*Artifact, bool)` — find the artifact entry whose Key matches.

- [ ] **Step 1: Precondition check — confirm the backup image has sha256sum**

Run: `git grep -n "sha256sum\|coreutils\|FROM " -- '*kuso-backup*' 'docker/**' 'images/**' 2>/dev/null; echo "---"; find . -path ./.git -prune -o -name 'Dockerfile*' -print | xargs grep -l -i "kuso-backup\|pg_dump\|redis-cli" 2>/dev/null`
Expected: locate the `kuso-backup` image Dockerfile. Open it and confirm `sha256sum` is available (it is part of `coreutils`; alpine's `busybox` also provides `sha256sum`). If the base is `alpine`/`busybox`, `sha256sum` is present — no change needed. If it is a distroless/scratch base, add `coreutils` and note it in the commit. Record the finding in the Task 1 commit message.

- [ ] **Step 2: Write the failing test**

```go
// server-go/internal/backup/manifest_test.go
package backup

import "testing"

func TestManifestKey(t *testing.T) {
	got := ManifestKey("acme/acme-db/20260721T120000Z.sql.gz")
	want := "acme/acme-db/20260721T120000Z.sql.gz.manifest.json"
	if got != want {
		t.Fatalf("ManifestKey = %q, want %q", got, want)
	}
}

func TestParseAndArtifactFor(t *testing.T) {
	raw := []byte(`{
	  "schemaVersion":1,"createdAt":"2026-07-21T12:00:00Z",
	  "project":"acme","addon":"acme-db","addonKind":"postgres","producer":"pg_dump",
	  "artifacts":[{"key":"acme/acme-db/x.sql.gz","sha256":"abc","bytes":42,"payloadKind":"pg_dump"}]
	}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.AddonKind != "postgres" || len(m.Artifacts) != 1 {
		t.Fatalf("parsed wrong: %#v", m)
	}
	a, ok := m.ArtifactFor("acme/acme-db/x.sql.gz")
	if !ok || a.SHA256 != "abc" || a.Bytes != 42 {
		t.Fatalf("ArtifactFor wrong: %#v %v", a, ok)
	}
	if _, ok := m.ArtifactFor("nope"); ok {
		t.Fatal("ArtifactFor should miss unknown key")
	}
}

func TestParseRejectsFutureSchema(t *testing.T) {
	if _, err := Parse([]byte(`{"schemaVersion":99}`)); err == nil {
		t.Fatal("expected error for future schemaVersion")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `cd server-go && go test ./internal/backup/ -v`
Expected: FAIL — package/functions undefined.

- [ ] **Step 4: Write minimal implementation**

```go
// server-go/internal/backup/manifest.go

// Package backup holds datastore-backup primitives shared across the
// HTTP handler and (later) the producer registry. This file defines the
// manifest written next to every backup artifact so a restore can verify
// integrity before applying.
package backup

import (
	"encoding/json"
	"fmt"
)

// SchemaVersion is the current manifest schema. A manifest with a higher
// version than this binary understands is rejected rather than
// mis-parsed.
const SchemaVersion = 1

// Artifact is one backed-up object plus its integrity metadata. No
// secret values are ever recorded here.
type Artifact struct {
	Key         string `json:"key"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"bytes"`
	PayloadKind string `json:"payloadKind"`
}

// Manifest describes one backup run and its artifacts.
type Manifest struct {
	SchemaVersion int        `json:"schemaVersion"`
	CreatedAt     string     `json:"createdAt"`
	Project       string     `json:"project"`
	Addon         string     `json:"addon"`
	AddonKind     string     `json:"addonKind"`
	Producer      string     `json:"producer"`
	Artifacts     []Artifact `json:"artifacts"`
}

// ManifestKey returns the S3 key of the manifest stored beside an
// artifact.
func ManifestKey(artifactKey string) string {
	return artifactKey + ".manifest.json"
}

// Parse unmarshals a manifest and rejects a schema newer than this
// binary supports.
func Parse(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if m.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("manifest schemaVersion %d newer than supported %d — upgrade kuso", m.SchemaVersion, SchemaVersion)
	}
	return &m, nil
}

// ArtifactFor finds the artifact entry for an S3 key.
func (m *Manifest) ArtifactFor(key string) (*Artifact, bool) {
	for i := range m.Artifacts {
		if m.Artifacts[i].Key == key {
			return &m.Artifacts[i], true
		}
	}
	return nil, false
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd server-go && go test ./internal/backup/ -v`
Expected: PASS (all three tests).

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/backup/manifest.go server-go/internal/backup/manifest_test.go
git commit -m "feat(backup): manifest type + package for integrity metadata

Confirms kuso-backup image provides sha256sum (<record finding from Step 1>).

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Write the manifest in the postgres + redis backup CronJobs

**Files:**
- Modify: `operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml` (postgres args block ~74-83; redis args block ~322-331)

**Interfaces:**
- Produces: for every scheduled backup, an additional S3 object `<KEY>.manifest.json` with `{schemaVersion,createdAt,project,addon,addonKind,producer,artifacts:[{key,sha256,bytes,payloadKind}]}`.
- Consumes: nothing new (same env the scripts already have: `BUCKET`,`S3_ENDPOINT`,`PROJECT`,`ADDON`,`KEY`,`PGPASSWORD`/`REDIS_PASSWORD` etc.).

- [ ] **Step 1: Replace the postgres dump+upload with a temp-file + manifest flow**

In the postgres args block, replace these lines:
```yaml
                  echo "==> dumping ${ADDON} → s3://${BUCKET}/${KEY}"
                  PGPASSWORD="${POSTGRES_PASSWORD}" pg_dump \
                    -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}" \
                    | gzip \
                    | aws s3 cp --endpoint-url "${S3_ENDPOINT}" - "s3://${BUCKET}/${KEY}"
                  echo "==> upload done"
```
with:
```yaml
                  echo "==> dumping ${ADDON} → s3://${BUCKET}/${KEY}"
                  PGPASSWORD="${POSTGRES_PASSWORD}" pg_dump \
                    -h "${POSTGRES_HOST}" -U "${POSTGRES_USER}" "${POSTGRES_DB}" \
                    | gzip > /tmp/dump.gz
                  SHA=$(sha256sum /tmp/dump.gz | awk '{print $1}')
                  BYTES=$(wc -c < /tmp/dump.gz | tr -d ' ')
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/dump.gz "s3://${BUCKET}/${KEY}"
                  echo "==> upload done (sha256=${SHA} bytes=${BYTES})"
                  # Integrity manifest written beside the artifact. Restore
                  # verifies the sha256 before applying. No secret values here.
                  cat > /tmp/manifest.json <<EOF
                  {"schemaVersion":1,"createdAt":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","project":"${PROJECT}","addon":"${ADDON}","addonKind":"postgres","producer":"pg_dump","artifacts":[{"key":"${KEY}","sha256":"${SHA}","bytes":${BYTES},"payloadKind":"pg_dump"}]}
                  EOF
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/manifest.json "s3://${BUCKET}/${KEY}.manifest.json"
                  rm -f /tmp/dump.gz /tmp/manifest.json
                  echo "==> manifest done"
```

- [ ] **Step 2: Replace the redis snapshot+upload with a temp-file + manifest flow**

In the redis args block, replace these lines:
```yaml
                  echo "==> snapshotting ${ADDON} → s3://${BUCKET}/${KEY}"
                  redis-cli -h "${REDIS_HOST}" -a "${REDIS_PASSWORD}" --no-auth-warning --rdb /tmp/dump.rdb
                  gzip -c /tmp/dump.rdb \
                    | aws s3 cp --endpoint-url "${S3_ENDPOINT}" - "s3://${BUCKET}/${KEY}"
                  rm -f /tmp/dump.rdb
                  echo "==> upload done"
```
with:
```yaml
                  echo "==> snapshotting ${ADDON} → s3://${BUCKET}/${KEY}"
                  redis-cli -h "${REDIS_HOST}" -a "${REDIS_PASSWORD}" --no-auth-warning --rdb /tmp/dump.rdb
                  gzip -c /tmp/dump.rdb > /tmp/dump.rdb.gz
                  SHA=$(sha256sum /tmp/dump.rdb.gz | awk '{print $1}')
                  BYTES=$(wc -c < /tmp/dump.rdb.gz | tr -d ' ')
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/dump.rdb.gz "s3://${BUCKET}/${KEY}"
                  echo "==> upload done (sha256=${SHA} bytes=${BYTES})"
                  cat > /tmp/manifest.json <<EOF
                  {"schemaVersion":1,"createdAt":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","project":"${PROJECT}","addon":"${ADDON}","addonKind":"redis","producer":"redis_rdb","artifacts":[{"key":"${KEY}","sha256":"${SHA}","bytes":${BYTES},"payloadKind":"redis_rdb"}]}
                  EOF
                  aws s3 cp --endpoint-url "${S3_ENDPOINT}" /tmp/manifest.json "s3://${BUCKET}/${KEY}.manifest.json"
                  rm -f /tmp/dump.rdb /tmp/dump.rdb.gz /tmp/manifest.json
                  echo "==> manifest done"
```

- [ ] **Step 3: Render the chart to verify the templates are valid YAML**

Run: `helm template test operator/helm-charts/kusoaddon --set kind=postgres --set backup.schedule='0 3 * * *' --set project=acme 2>&1 | grep -A2 'manifest.json' | head`
Expected: the rendered CronJob shows the `aws s3 cp ... .manifest.json` line; no template/YAML error. Repeat with `--set kind=redis`.

Note: if `helm` is unavailable locally, instead run a YAML lint on the file: `python3 -c "import yaml,sys; [yaml.safe_load(d) for d in open('operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml').read().split('---') if d.strip() and '{{' not in d]" ` is NOT reliable (templates contain `{{}}`). Prefer `helm template`. If neither works, visually diff the indentation against the surrounding block (8 spaces of script indent under `- |`).

- [ ] **Step 4: Commit**

```bash
git add operator/helm-charts/kusoaddon/templates/backup-cronjob.yaml
git commit -m "feat(backup): write sha256 manifest beside pg/redis backups

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Verify the manifest in the restore Job

**Files:**
- Modify: `server-go/internal/http/handlers/backups.go` (the restore Job `Args` script, ~450-457)

**Interfaces:**
- Consumes: `backup.ManifestKey` (Task 1) is NOT used in the shell (the Job derives the manifest key inline as `${KEY}.manifest.json`), but the Go handler imports the `backup` package for the shared constant/type if needed. The script gains a verify step.
- Produces: a restore that aborts (non-zero exit, before touching the DB) on checksum mismatch; warns + proceeds when no manifest exists.

- [ ] **Step 1: Write the failing test (script contains the verify step)**

Because the restore Job script is a string built in Go, assert the script text includes the verification logic. Add to a test file:
```go
// server-go/internal/http/handlers/backups_restore_script_test.go
package handlers

import (
	"strings"
	"testing"
)

func TestRestoreScriptVerifiesChecksum(t *testing.T) {
	s := restoreScript()
	for _, want := range []string{
		"manifest.json",
		"sha256sum",
		"MISMATCH",
		"no manifest", // backward-compat warning branch
	} {
		if !strings.Contains(s, want) {
			t.Errorf("restore script missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestRestoreScriptVerifiesChecksum -v`
Expected: FAIL — `restoreScript` undefined (the script is currently an inline literal, not a function).

- [ ] **Step 3: Extract the restore script into a function with the verify step**

In `backups.go`, replace the inline `Args: []string{` + heredoc block in the restore Job with `Args: []string{restoreScript()},` and add this function near the handler (keep the `${KEY}`/env contract identical):
```go
// restoreScript is the shell the restore Job runs. It downloads the
// artifact AND its sibling manifest, verifies the artifact's sha256
// against the manifest before applying (aborting on mismatch), and
// warns-but-proceeds when a pre-manifest backup has no manifest.
func restoreScript() string {
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
```
Then change the Job container's `Args` to:
```go
						Args:            []string{restoreScript()},
```

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestRestoreScriptVerifiesChecksum -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/http/handlers/backups.go server-go/internal/http/handlers/backups_restore_script_test.go
git commit -m "feat(backup): verify sha256 manifest on restore, abort on mismatch

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Keep manifests out of the backup List

**Files:**
- Modify: `server-go/internal/http/handlers/backups.go` (the `List` handler loop, ~266-275)
- Test: add to `server-go/internal/http/handlers/backups_restore_script_test.go` (same file, no new file needed)

**Interfaces:**
- Produces: `List` skips `.manifest.json` objects so they don't appear as restorable backups. Extract the filter into a tiny testable helper `func isManifestKey(key string) bool`.

- [ ] **Step 1: Write the failing test**

```go
// append to server-go/internal/http/handlers/backups_restore_script_test.go
func TestIsManifestKey(t *testing.T) {
	if !isManifestKey("acme/acme-db/x.sql.gz.manifest.json") {
		t.Error("manifest key should be detected")
	}
	if isManifestKey("acme/acme-db/x.sql.gz") {
		t.Error("artifact key is not a manifest")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/http/handlers/ -run TestIsManifestKey -v`
Expected: FAIL — `isManifestKey` undefined.

- [ ] **Step 3: Add the helper and use it in List**

Add near the List handler:
```go
// isManifestKey reports whether an S3 key is a backup manifest sidecar
// rather than a restorable artifact.
func isManifestKey(key string) bool {
	return strings.HasSuffix(key, ".manifest.json")
}
```
In the `List` loop, right after `key := aws.ToString(o.Key)`, add:
```go
		if isManifestKey(key) {
			continue // sidecar, not a restorable backup
		}
```

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/http/handlers/ -run 'TestIsManifestKey|TestRestoreScriptVerifiesChecksum' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/http/handlers/backups.go server-go/internal/http/handlers/backups_restore_script_test.go
git commit -m "fix(backup): exclude .manifest.json sidecars from backup list

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Package tests, docs, rollout note

**Files:**
- Modify: `docs/EDIT_SAFETY.md` (note the manifest/verify guarantee if a backup section exists; else skip)
- Modify: `server-go` — full build + test
- Create/Modify: a short note in the design doc's status or a rollout comment

- [ ] **Step 1: Full server-go build + backup-related tests**

Run: `cd server-go && go build ./... && go test ./internal/backup/ ./internal/http/handlers/ 2>&1 | tail -20`
Expected: build clean; `internal/backup` PASS; handlers package PASS (or only pre-existing unrelated failures — note any that predate this work).

- [ ] **Step 2: Document the rollout requirement**

The manifest-write change lives in the addon helm chart, so EXISTING addons keep using their old CronJob until their chart is re-rendered/upgraded. Add a note to the design doc (`docs/superpowers/specs/2026-07-21-openship-inspired-improvements-design.md`) under Piece 2, or create `docs/superpowers/notes/backup-manifest-rollout.md` with:
```markdown
# Backup manifest rollout

- New backups get a manifest only after the addon's helm release is
  upgraded to the chart carrying the Task 2 CronJob change. Trigger an
  addon update (or the operator's next reconcile) to re-render.
- Restore is backward-compatible: pre-manifest backups restore with a
  "integrity NOT verified" warning; no action needed.
- No server-go release is required for the backup-side change (it's the
  chart), but the restore-side verify ships in the server-go binary — so
  `make ship` is needed for restore verification to take effect.
```

- [ ] **Step 3: Commit**

```bash
git add docs/
git commit -m "docs(backup): manifest/verify rollout note

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage** (Piece 2 of the design):
- `manifest.json` with per-artifact sha256/size/kind → Task 1 (type) + Task 2 (written by CronJob). ✓
- No secret values in manifest → Task 2 scripts record only key/sha/bytes/kind. ✓
- Verified on restore, abort on mismatch → Task 3 (`restoreScript` verify + `exit 1`). ✓
- Backward-compatible (missing manifest → warn + proceed) → Task 3 else-branch. ✓
- One manifest per run, artifacts as a list → Task 1 `Artifacts []Artifact`. ✓
- Don't surface manifests as backups → Task 4 (`isManifestKey` filter in List). ✓
- Temp-file approach (design decision) → Task 2 scripts dump to `/tmp` then hash. ✓

**2. Placeholder scan:** No TBD/TODO. Task 1 Step 1 has a `<record finding>` in the commit message — that's an instruction to fill in an observed fact, not a code placeholder. Every code step has complete code + commands with expected output. ✓

**3. Type consistency:** `Manifest`/`Artifact` JSON field names in Task 1 (`sha256`,`bytes`,`payloadKind`,`addonKind`,`producer`) exactly match the JSON the Task 2 scripts emit (`"sha256":`,`"bytes":`,`"payloadKind":`,`"addonKind":`,`"producer":`). The restore script (Task 3) greps `"sha256":"..."` which matches the emitted quoting. `isManifestKey` suffix `.manifest.json` matches `ManifestKey`'s append. ✓

Note for the implementer: `strings` must be imported in `backups.go` — it already is (used by the existing key guards), so Task 4's `strings.HasSuffix` needs no new import. Verify with `goimports`/build.
