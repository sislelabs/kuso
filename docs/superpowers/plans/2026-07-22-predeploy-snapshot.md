# Pre-deploy Postgres Snapshot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When an opt-in service deploys and has a release hook, snapshot its subscribed postgres addon(s) BEFORE the release-hook Job runs; if the hook fails, keep the old image (existing behavior) AND surface a restore-to-pre-deploy-snapshot action. A snapshot infra-failure blocks the deploy.

**Architecture:** A new `spec.snapshotBeforeDeploy` bool on KusoService, mirrored to KusoEnvironment (like `Release`). A `Snapshotter` interface on the build Poller (mirroring `ReleaseRunner`), wired in main.go. In the promote loop of `builds.go`, inside the `shouldRunRelease` block and before `ReleaseRunner.Run`, call the snapshotter for each subscribed postgres addon; record snapshot keys on the build; on snapshot infra-fail set `releaseBlocked` and skip; on release-hook fail, add the snapshot keys to the failure notify + build annotation.

**Tech Stack:** Go, Kubernetes CRD YAML, Helm (chart value passthrough), the existing backup Job machinery.

## Global Constraints

- Snapshot is **postgres-only**. Targets = the env's subscribed addons (`env.Spec.EnvFromSecrets` holds `<addon>-conn` secret names) whose addon kind is `postgres`.
- Snapshot only fires when BOTH `snapshotBeforeDeploy` is true AND `shouldRunRelease(env)` is true (a release hook exists). Flag on + no hook = no snapshot.
- `snapshotBeforeDeploy` is a service-spec field: it MUST be added to (a) `KusoServiceSpec` + CRD YAML, (b) `KusoEnvironmentSpec` + CRD YAML, (c) the AddService env literal in `services_ops.go` (~line 475, next to `Release`), and (d) the propagate path in `propagate.go` (~line 309, next to `changed.Release`) AND `changedFields` (~line 64/94). Missing any of these silently drops the field (see the addservice-env-literal-drops-fields learning).
- Snapshot infra-failure → treat like a release infra error: `releaseBlocked = true`, skip promote, retry next tick. Do NOT proceed into the migration if the promised safety net couldn't be taken.
- Reuse the manifest/producer machinery from Pieces 2–3. A pre-deploy snapshot is a normal postgres backup with `trigger:"pre_deploy"` recorded, so it appears in the existing backup list and restores via the existing (verified) restore path.
- The `Snapshotter` field on Poller is nil-able: nil → snapshots silently skipped (test fixtures, legacy main.go), exactly like `ReleaseRunner`.
- After server-go changes: `cd server-go && go build ./... && go test ./internal/builds/ ./internal/projects/` pass. CRD YAML changes require `kubectl apply` on the live cluster (call out in rollout; auto-updater only flips image tags).

---

### Task 1: Add `snapshotBeforeDeploy` to the spec types + CRD YAML

**Files:**
- Modify: `server-go/internal/kube/types.go` (KusoServiceSpec ~330; KusoEnvironmentSpec ~707)
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml`
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml`
- Test: `server-go/internal/kube/types_snapshot_test.go`

**Interfaces:**
- Produces: `KusoServiceSpec.SnapshotBeforeDeploy bool` (json `snapshotBeforeDeploy,omitempty`) and the same on `KusoEnvironmentSpec`.

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/kube/types_snapshot_test.go
package kube

import (
	"encoding/json"
	"testing"
)

func TestSnapshotBeforeDeployRoundTrips(t *testing.T) {
	s := KusoServiceSpec{SnapshotBeforeDeploy: true}
	b, _ := json.Marshal(s)
	var back KusoServiceSpec
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !back.SnapshotBeforeDeploy {
		t.Fatalf("service snapshotBeforeDeploy lost in round-trip: %s", b)
	}
	e := KusoEnvironmentSpec{SnapshotBeforeDeploy: true}
	eb, _ := json.Marshal(e)
	var eBack KusoEnvironmentSpec
	if err := json.Unmarshal(eb, &eBack); err != nil {
		t.Fatal(err)
	}
	if !eBack.SnapshotBeforeDeploy {
		t.Fatalf("env snapshotBeforeDeploy lost in round-trip: %s", eb)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/kube/ -run TestSnapshotBeforeDeploy -v`
Expected: FAIL — `SnapshotBeforeDeploy` undefined.

- [ ] **Step 3: Add the fields**

In `types.go`, after the `Release *KusoReleaseSpec` line in `KusoServiceSpec` (~330):
```go
	// SnapshotBeforeDeploy, when true, snapshots the service's subscribed
	// postgres addon(s) BEFORE the release hook (migration) runs, so a bad
	// migration has a one-click restore. Only fires when a release hook is
	// also present. Mirrored onto every owned env.
	SnapshotBeforeDeploy bool `json:"snapshotBeforeDeploy,omitempty"`
```
In `KusoEnvironmentSpec`, after its `Release *KusoReleaseSpec` line (~707):
```go
	// SnapshotBeforeDeploy mirrors KusoServiceSpec.SnapshotBeforeDeploy so
	// the build poller can read it off the env CR. Server-managed.
	SnapshotBeforeDeploy bool `json:"snapshotBeforeDeploy,omitempty"`
```

- [ ] **Step 4: Add to both CRD YAMLs**

In each CRD YAML, under `spec.properties` (locate the `release:` property block and add a sibling), add:
```yaml
                snapshotBeforeDeploy:
                  type: boolean
```
(Match the existing indentation of sibling scalar properties in that file — find `release:` and align to it. If the CRD uses `x-kubernetes-preserve-unknown-fields: true` at spec level, this addition is still good hygiene but not strictly required — add it anyway for schema clarity.)

- [ ] **Step 5: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/kube/ -run TestSnapshotBeforeDeploy -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/kube/types.go server-go/internal/kube/types_snapshot_test.go operator/config/crd/bases/application.kuso.sislelabs.com_kusoservices.yaml operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml
git commit -m "feat(snapshot): add snapshotBeforeDeploy to service+env spec/CRD

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Propagate `snapshotBeforeDeploy` service→env

**Files:**
- Modify: `server-go/internal/projects/services_ops.go` (AddService env literal ~475; update changedFields ~2296)
- Modify: `server-go/internal/projects/propagate.go` (`changedFields` struct ~64; `Any()`-style aggregate ~94; propagate block ~309)
- Test: `server-go/internal/projects/snapshot_propagate_test.go`

**Interfaces:**
- Consumes: `svc.Spec.SnapshotBeforeDeploy` (Task 1).
- Produces: a newly-created env carries the service's `SnapshotBeforeDeploy`; an update that toggles it re-stamps every owned env.

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/projects/snapshot_propagate_test.go
package projects

import "testing"

func TestSnapshotFieldInChangedAggregate(t *testing.T) {
	c := changedFields{Snapshot: true}
	if !c.any() {
		t.Fatal("changedFields.any() must be true when Snapshot changed")
	}
}
```
(If the aggregate method is named differently — verify by reading `propagate.go:94` — use that name. The line shows `func (c changedFields) <name>() bool { return c.EnvVars || ... }`.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/projects/ -run TestSnapshotFieldInChangedAggregate -v`
Expected: FAIL — `Snapshot` field undefined.

- [ ] **Step 3: Add the field to changedFields + aggregate + propagate + literals**

In `propagate.go`, in `type changedFields struct` (~64, after `Release bool`):
```go
	// Snapshot carries spec.snapshotBeforeDeploy changes so the poller's
	// pre-deploy snapshot decision tracks the current service spec.
	Snapshot bool
```
In the aggregate boolean (~94), append ` || c.Snapshot`.
In the propagate block (~309, right after the `if changed.Release { env.Spec.Release = svc.Spec.Release }`):
```go
			if changed.Snapshot {
				env.Spec.SnapshotBeforeDeploy = svc.Spec.SnapshotBeforeDeploy
			}
```
In `services_ops.go` AddService env literal (~475, right after `Release: releaseSpec,`):
```go
			SnapshotBeforeDeploy: req.SnapshotBeforeDeploy,
```
And in the second env literal (~643, after `Release: created.Spec.Release,`):
```go
			SnapshotBeforeDeploy: created.Spec.SnapshotBeforeDeploy,
```
In the update-path `changedFields` literal (~2296, after `Release: releaseChanged,`):
```go
			Snapshot:          snapshotChanged,
```
Compute `snapshotChanged` next to `releaseChanged` in the update handler (find where `releaseChanged` is set — it compares old vs new spec; add `snapshotChanged := old.Spec.SnapshotBeforeDeploy != svc.Spec.SnapshotBeforeDeploy` in the same block). Also add `SnapshotBeforeDeploy` to the `CreateServiceRequest`/`UpdateServiceRequest` struct + the service-spec assignment so the API accepts it — find `req.Release`/`releaseSpec` origin and mirror it. (Search: `grep -n "SnapshotBeforeDeploy\|releaseSpec\|req.Release" services_ops.go`.)

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/projects/ -run TestSnapshotFieldInChangedAggregate -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/projects/
git commit -m "feat(snapshot): propagate snapshotBeforeDeploy service->env

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `Snapshotter` interface + pre-deploy hook in builds.go

**Files:**
- Modify: `server-go/internal/builds/builds.go` (Poller struct ~1122; interface near `ReleaseRunner` ~2813; promote loop ~2657)
- Test: `server-go/internal/builds/snapshot_test.go`

**Interfaces:**
- Produces:
  - `type Snapshotter interface { Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error) }` — returns the S3 keys of the snapshots taken (one per subscribed postgres addon), or an error on infra failure.
  - `Poller.Snapshotter Snapshotter` field (nil-able).
  - Behavior: in the promote loop, inside `if shouldRunRelease(&e) && p.ReleaseRunner != nil`, BEFORE `ReleaseRunner.Run`: if `e.Spec.SnapshotBeforeDeploy && p.Snapshotter != nil`, call `Snapshot`; on error set `releaseBlocked = true` + `continue`; on success stash keys for the release-fail path.

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/builds/snapshot_test.go
package builds

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

type fakeSnapshotter struct {
	called bool
	keys   []string
	err    error
}

func (f *fakeSnapshotter) Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error) {
	f.called = true
	return f.keys, f.err
}

func TestSnapshotDecision(t *testing.T) {
	env := &kube.KusoEnvironment{}
	env.Spec.SnapshotBeforeDeploy = true
	env.Spec.Release = &kube.KusoReleaseSpec{Command: []string{"migrate"}}

	// flag on + hook present -> should snapshot
	if !shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("expected snapshot when flag on + hook present + snapshotter set")
	}
	// flag off -> no snapshot
	env.Spec.SnapshotBeforeDeploy = false
	if shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("flag off should mean no snapshot")
	}
	// flag on, no snapshotter -> no snapshot
	env.Spec.SnapshotBeforeDeploy = true
	if shouldSnapshot(env, nil) {
		t.Error("nil snapshotter should mean no snapshot")
	}
	// flag on, no release hook -> no snapshot
	env.Spec.Release = nil
	if shouldSnapshot(env, &fakeSnapshotter{}) {
		t.Error("no release hook should mean no snapshot")
	}
}

func TestSnapshotInfraFailBlocks(t *testing.T) {
	fs := &fakeSnapshotter{err: errors.New("s3 down")}
	_, err := runPredeploySnapshot(context.Background(), "ns", &kube.KusoEnvironment{}, fs)
	if err == nil {
		t.Fatal("snapshot infra error must propagate so the caller blocks the deploy")
	}
	if !fs.called {
		t.Fatal("snapshotter should have been called")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/builds/ -run TestSnapshot -v`
Expected: FAIL — `shouldSnapshot`, `runPredeploySnapshot`, `Snapshotter` undefined.

- [ ] **Step 3: Add the interface, field, helpers, and hook**

Near the `ReleaseRunner` interface (~2813) add:
```go
// Snapshotter takes a pre-deploy backup of an env's subscribed postgres
// addon(s). Returns the S3 keys of the snapshots taken. Defined locally
// so builds doesn't import the backup handler package directly.
type Snapshotter interface {
	Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error)
}

// shouldSnapshot reports whether a pre-deploy snapshot should run for this
// env: the opt-in flag is set, a snapshotter is wired, and a release hook
// exists (the risk surface). No hook = no migration = nothing to guard.
func shouldSnapshot(e *kube.KusoEnvironment, s Snapshotter) bool {
	return e.Spec.SnapshotBeforeDeploy && s != nil && shouldRunRelease(e)
}

// runPredeploySnapshot invokes the snapshotter and returns the keys taken.
// A non-nil error means the caller must NOT proceed into the migration.
func runPredeploySnapshot(ctx context.Context, ns string, e *kube.KusoEnvironment, s Snapshotter) ([]string, error) {
	return s.Snapshot(ctx, ns, e)
}
```
Add to the Poller struct (~1122, after `ReleaseRunner ReleaseRunner`):
```go
	// Snapshotter takes a pre-deploy backup of subscribed postgres addons
	// before the release hook runs, when the env opts in via
	// spec.snapshotBeforeDeploy. Optional: nil → snapshots skipped.
	Snapshotter Snapshotter
```
In the promote loop, change the release block (~2657) to snapshot first:
```go
		if shouldRunRelease(&e) && p.ReleaseRunner != nil {
			var snapKeys []string
			if shouldSnapshot(&e, p.Snapshotter) {
				keys, serr := runPredeploySnapshot(ctx, ns, &e, p.Snapshotter)
				if serr != nil {
					// Promised a safety net but couldn't take it — do NOT
					// proceed into the migration. Block + retry next tick.
					releaseBlocked = true
					p.logger().Error("pre-deploy snapshot failed — blocking deploy",
						"env", e.Name, "build", b.Name, "err", serr)
					continue
				}
				snapKeys = keys
				p.logger().Info("pre-deploy snapshot taken",
					"env", e.Name, "build", b.Name, "keys", snapKeys)
			}
			res, rerr := p.ReleaseRunner.Run(ctx, ns, &e, b.Spec.Image)
			if rerr != nil {
				releaseBlocked = true
				p.logger().Error("release hook run failed (infra error)",
					"env", e.Name, "build", b.Name, "err", rerr)
				continue
			}
			if res.Outcome != releaserun.OutcomeSucceeded {
				releaseBlocked = true
				p.logger().Warn("release hook failed — skipping image promote",
					"env", e.Name, "build", b.Name, "outcome", res.Outcome, "job", res.JobName, "msg", res.Message)
				p.markReleaseFailedWithSnapshot(ctx, ns, b, &e, res, snapKeys)
				continue
			}
			p.logger().Info("release hook succeeded",
				"env", e.Name, "build", b.Name, "job", res.JobName)
		}
```
(Note: `markReleaseFailed` becomes `markReleaseFailedWithSnapshot` — Task 4 adds the snapshot-key surfacing; for THIS task, add a thin shim so the build compiles: `func (p *Poller) markReleaseFailedWithSnapshot(ctx, ns, b, e, res, snapKeys) { p.markReleaseFailed(ctx, ns, b, e, res) }` and flesh it out in Task 4.)

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/builds/ -run TestSnapshot -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/builds/builds.go server-go/internal/builds/snapshot_test.go
git commit -m "feat(snapshot): Snapshotter seam + pre-deploy hook in build poller

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Surface the restore action on release-failure

**Files:**
- Modify: `server-go/internal/builds/builds.go` (`markReleaseFailedWithSnapshot` + notify)
- Test: `server-go/internal/builds/snapshot_test.go` (append)

**Interfaces:**
- Produces: on release-hook failure after a snapshot, the build CR gets an annotation `kuso.sislelabs.com/predeploy-snapshot-keys` (comma-joined keys) and the failure notify message names the restore path.

- [ ] **Step 1: Write the failing test**

```go
// append to server-go/internal/builds/snapshot_test.go
func TestSnapshotKeysAnnotation(t *testing.T) {
	got := snapshotKeysAnnotationValue([]string{"a/b/1.sql.gz", "a/c/2.sql.gz"})
	if got != "a/b/1.sql.gz,a/c/2.sql.gz" {
		t.Fatalf("annotation value = %q", got)
	}
	if snapshotKeysAnnotationValue(nil) != "" {
		t.Fatal("nil keys -> empty annotation value")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/builds/ -run TestSnapshotKeysAnnotation -v`
Expected: FAIL — `snapshotKeysAnnotationValue` undefined.

- [ ] **Step 3: Implement the helper + flesh out the shim**

```go
const annPredeploySnapshotKeys = "kuso.sislelabs.com/predeploy-snapshot-keys"

// snapshotKeysAnnotationValue joins snapshot keys for the build annotation.
func snapshotKeysAnnotationValue(keys []string) string {
	return strings.Join(keys, ",")
}

// markReleaseFailedWithSnapshot marks the build release-failed and, when a
// pre-deploy snapshot was taken, records the snapshot keys on the build so
// the UI/CLI can offer a restore-to-pre-deploy action.
func (p *Poller) markReleaseFailedWithSnapshot(ctx context.Context, ns string, b *kube.KusoBuild, e *kube.KusoEnvironment, res releaserun.Result, snapKeys []string) {
	p.markReleaseFailed(ctx, ns, b, e, res)
	if len(snapKeys) == 0 {
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`,
		annPredeploySnapshotKeys, snapshotKeysAnnotationValue(snapKeys))
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		p.logger().Error("annotate build with snapshot keys", "build", b.Name, "err", err)
	}
}
```
(Confirm `strings` is imported in builds.go; if not, add it. Confirm the `types.MergePatchType` + `metav1` + `kube.GVRBuilds` usage matches the existing `markSucceeded` patch call — copy that exact pattern.)

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/builds/ -run TestSnapshot -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/builds/builds.go server-go/internal/builds/snapshot_test.go
git commit -m "feat(snapshot): record snapshot keys on release-failed build

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Concrete Snapshotter (backup Job per subscribed postgres addon)

**Files:**
- Create: `server-go/internal/builds/snapshotter.go` (or a small adapter package) — the concrete `Snapshotter` that lists an env's subscribed postgres addons and creates a backup Job each.
- Modify: wherever the Poller is constructed in `main.go` (wire `Snapshotter`).
- Test: `server-go/internal/builds/snapshotter_test.go`

**Interfaces:**
- Consumes: an addons lister + the backup Job creation path.
- Produces: `func addonNamesFromEnvFromSecrets(env *kube.KusoEnvironment) []string` — maps each `<addon>-conn` entry to `<addon>`; the concrete Snapshotter filters those to kind==postgres and creates a snapshot backup Job per addon, returning the S3 keys.

- [ ] **Step 1: Write the failing test**

```go
// server-go/internal/builds/snapshotter_test.go
package builds

import (
	"testing"

	"kuso/server/internal/kube"
)

func TestAddonNamesFromEnvFromSecrets(t *testing.T) {
	env := &kube.KusoEnvironment{}
	env.Spec.EnvFromSecrets = []string{"acme-db-conn", "acme-cache-conn", "not-a-conn"}
	got := addonNamesFromEnvFromSecrets(env)
	// only the "-conn" suffixed entries map to addon names
	want := map[string]bool{"acme-db": true, "acme-cache": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 addon names", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected addon name %q", n)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd server-go && go test ./internal/builds/ -run TestAddonNamesFromEnvFromSecrets -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement the parser + concrete Snapshotter**

```go
// server-go/internal/builds/snapshotter.go
package builds

import (
	"strings"
)

// addonNamesFromEnvFromSecrets extracts addon names from an env's
// EnvFromSecrets list, whose entries are "<addon>-conn" secret names.
func addonNamesFromEnvFromSecrets(env *kube.KusoEnvironment) []string {
	var out []string
	for _, s := range env.Spec.EnvFromSecrets {
		if name, ok := strings.CutSuffix(s, "-conn"); ok && name != "" {
			out = append(out, name)
		}
	}
	return out
}
```
Then the concrete Snapshotter: define an interface for the two dependencies it needs so it stays testable —
```go
// AddonKindLister reports an addon's kind. SnapshotJobCreator creates a
// backup Job for one addon and returns the S3 key it will write.
type AddonKindLister interface {
	AddonKind(ctx context.Context, project, addon string) (string, error)
}
type SnapshotJobCreator interface {
	CreateSnapshotJob(ctx context.Context, project, addon, trigger, buildRef string) (key string, err error)
}

// PredeploySnapshotter is the concrete Snapshotter wired in main.go.
type PredeploySnapshotter struct {
	Kinds AddonKindLister
	Jobs  SnapshotJobCreator
}

func (s *PredeploySnapshotter) Snapshot(ctx context.Context, ns string, env *kube.KusoEnvironment) ([]string, error) {
	project := env.Spec.Project // confirm the field name holding the project
	var keys []string
	for _, addon := range addonNamesFromEnvFromSecrets(env) {
		kind, err := s.Kinds.AddonKind(ctx, project, addon)
		if err != nil {
			return nil, fmt.Errorf("resolve addon %q kind: %w", addon, err)
		}
		if kind != "postgres" {
			continue // postgres-only per design
		}
		key, err := s.Jobs.CreateSnapshotJob(ctx, project, addon, "pre_deploy", env.Name)
		if err != nil {
			return nil, fmt.Errorf("snapshot addon %q: %w", addon, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}
```
(Verify `env.Spec.Project` is the right field for the project name — grep `Project` in the KusoEnvironmentSpec; adjust if it's derived differently, e.g. from a label. Add the needed imports: `context`, `fmt`.)

The `SnapshotJobCreator` concrete impl reuses the backup Job pattern from `backups.go` — extract a shared helper OR implement a minimal Job builder here that mirrors the postgres backup CronJob (mongo/registry from Piece 3 gives the producer, but for a snapshot we invoke the same `pg_dump | gzip | sha256 | s3 cp + manifest` flow with `KEY=<project>/<addon>/<ts>.sql.gz`). Keep the Job creation in the handler/backup package and pass an adapter in. **Minimize duplication**: prefer exposing a `CreateBackupJob(ctx, project, addon)` from the backups package and adapting it here.

- [ ] **Step 4: Run test to verify it passes + build**

Run: `cd server-go && go test ./internal/builds/ -run 'TestAddonNames|TestSnapshot' -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Wire the Snapshotter in main.go**

Find where `builds.Poller{...}` (or `builds.New`) is constructed in `main.go`/wherever the poller is assembled (grep `ReleaseRunner:`). Add `Snapshotter: &builds.PredeploySnapshotter{Kinds: <adapter>, Jobs: <adapter>}` alongside `ReleaseRunner`. If the adapters need the addons service + backup job creator, construct them from the same kube client used for `ReleaseRunner`.

- [ ] **Step 6: Build + commit**

Run: `cd server-go && go build ./...`
```bash
git add server-go/internal/builds/snapshotter.go server-go/internal/builds/snapshotter_test.go server-go/cmd/  # adjust main.go path
git commit -m "feat(snapshot): concrete pre-deploy snapshotter + main.go wiring

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: CLI + web surface + docs

**Files:**
- Modify: `cli/pkg/kusoApi/` + `cli/cmd/kusoCli/service*.go` (accept `--snapshot-before-deploy` on create/update; show it in get)
- Modify: web service feature (create/update form toggle + build view restore action) — `web/src/features/services/` + the build/deployment view
- Modify: `docs/EDIT_SAFETY.md` (snapshotBeforeDeploy: live-editable, no restart)
- Modify: `docs/superpowers/notes/backup-manifest-rollout.md` (piece 4 note)

- [ ] **Step 1: CLI flag**

Add `--snapshot-before-deploy` (bool) to the service create + update cobra commands; thread into the request struct sent to the API. Follow the existing `--release`-equivalent flag wiring in the service command. Build: `cd cli && go build -o /tmp/kuso ./cmd`.

- [ ] **Step 2: Web toggle + restore action**

Add a `snapshotBeforeDeploy` toggle to the service settings form (near the release-command field) and, on the build/deployment view, when a build has the `predeploy-snapshot-keys` annotation and is release-failed, render a "Restore pre-deploy snapshot" button that calls the existing restore endpoint with the first snapshot key. Follow existing form + mutation patterns (`web/src/features/services/{api,hooks}.ts`).

- [ ] **Step 3: Docs**

`docs/EDIT_SAFETY.md`: add a row — `snapshotBeforeDeploy` — live-editable, no restart, no data impact (it only affects future deploys). Append a Piece 4 section to the rollout note describing the flag, the postgres-only scope, and the CRD-apply requirement.

- [ ] **Step 4: Build/test/commit**

Run: `cd server-go && go build ./... && go test ./internal/builds/ ./internal/projects/ ./internal/kube/`; `cd cli && go build -o /tmp/kuso ./cmd`; web typecheck per repo convention.
```bash
git add cli/ web/ docs/
git commit -m "feat(snapshot): CLI/web surface + docs for snapshotBeforeDeploy

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**1. Spec coverage** (Piece 4):
- Snapshot before release-hook, opt-in, postgres-only → Tasks 1–3, 5. ✓
- Snapshot infra-fail blocks deploy → Task 3 (`releaseBlocked=true; continue`). ✓
- Release-hook fail surfaces restore action (not auto) → Task 4 (annotation + notify) + Task 6 (web button). ✓
- No snapshot when flag off or no hook → Task 3 `shouldSnapshot`. ✓
- Double-write to propagate + AddService literal → Task 2 (explicitly both). ✓
- Reuse manifest/restore path → Task 5 (`pre_deploy` trigger, same backup Job). ✓

**2. Placeholder scan:** Task 5 has two "confirm/verify the field name" notes (`env.Spec.Project`, the aggregate method name) — these are verification instructions, not placeholders, and each names the exact grep to resolve it. Task 6's web work is described by pattern-to-follow rather than full code because it's UI glue across files the plan can't fully enumerate blind; the server contract it consumes (annotation name, restore endpoint) is fully specified. Acceptable.

**3. Type consistency:** `Snapshotter.Snapshot(ctx, ns, env) ([]string, error)` consistent across interface (Task 3), fake (Task 3 test), and concrete (Task 5). `SnapshotBeforeDeploy` field name identical across types.go, propagate, literals, and builds.go reads. `annPredeploySnapshotKeys` const shared Task 4↔6. `shouldSnapshot`/`runPredeploySnapshot`/`markReleaseFailedWithSnapshot` names consistent between Task 3 and Task 4.

Deviation logged: Task 3 introduces `markReleaseFailedWithSnapshot` as a shim that Task 4 fills in — flagged in Task 3 Step 3 so an out-of-order implementer adds the shim.
