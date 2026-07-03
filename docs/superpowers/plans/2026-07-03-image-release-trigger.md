# Image-path Release Trigger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make the pre-deploy release hook (DB migrations) run for `runtime: image` services — today it only fires from the build poller, which image services skip, so marketplace apps that need a migration (Plausible) crash on first boot.

**Architecture:** Withhold the image from the env CR when the service has a release hook — stash it in a new `spec.pendingImage` field so the chart holds the pod at 0 replicas (reusing the existing "awaiting first build" hold). A new leader-elected `imagerelease` watcher reconciles envs with a `pendingImage`: runs the release Job via `releaserun.Runner`, and on success CAS-promotes `pendingImage → image` (chart scales the pod up). On failure the image stays withheld and the failure is surfaced. Built runtimes are untouched.

**Tech Stack:** Go (server-go: internal/kube, internal/projects, internal/imagerelease [new], internal/releaserun [reuse]), operator CRD YAML, leader-elected singleton wiring in cmd/kuso-server/main.go.

## Global Constraints

- **No behavior change when there's no release hook.** A `runtime: image`
  service with empty `spec.release` stamps `env.spec.image` immediately, exactly
  as today. `pendingImage` is only ever written when a release hook exists.
  This is the safety property — verify it in every task that touches the write path.
- **Reuse `releaserun`.** `releaserun.New(kc)` → `Runner.Run(ctx, ns, env, image) (Result, error)`. `Result.Outcome ∈ {OutcomeSucceeded, OutcomeFailed, OutcomeTimedOut}`. The Runner is idempotent per (env, image.Tag) via `releaserun.JobName` and waits for addons via its init container. Do NOT reimplement the Job.
- **Preview parity.** `kind: preview` envs never carry `pendingImage` (preview migrations are owned by the seed path — same exclusion `shouldRunRelease` makes).
- **Module `kuso/server`**; imports `kuso/server/internal/...`.
- **`KusoImage` shape:** `{Repository, Tag, PullPolicy string}` (all json-omitempty).
- **Watcher pattern:** mirror `internal/scaledown` — a `Watcher` struct with `Kube *kube.Client`, `Namespace string`, `Logger *slog.Logger`, `Tick time.Duration`, and `Run(ctx context.Context)`; leader-gated via `leader.RunWhenLeader`'s `startSingletons` in main.go.

---

## File Structure

**Modified:**
- `server-go/internal/kube/types.go` — add `PendingImage *KusoImage` to `KusoEnvironmentSpec`.
- `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml` — `pendingImage` schema.
- `server-go/internal/projects/services_ops.go` — AddService (create) + image-patch path: withhold image → pendingImage when a release hook is present.
- `server-go/cmd/kuso-server/main.go` — wire the watcher into `startSingletons`.

**Created:**
- `server-go/internal/imagerelease/watcher.go` — `Watcher` + `Run` loop + promote CAS.
- `server-go/internal/imagerelease/watcher_test.go`.
- `server-go/internal/projects/image_release_withhold_test.go` — AddService withhold behavior.

---

## Task 1: Add `PendingImage` to the env spec + CRD

**Files:**
- Modify: `server-go/internal/kube/types.go`
- Modify: `operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml`

**Interfaces:**
- Produces: `KusoEnvironmentSpec.PendingImage *KusoImage \`json:"pendingImage,omitempty"\``

- [ ] **Step 1: Add the field** to `KusoEnvironmentSpec` (next to its existing `Image *KusoImage` field, ~line 493):

```go
	// PendingImage holds a runtime=image target that is NOT YET LIVE
	// because the service has a release hook that must run first. The
	// imagerelease watcher runs the release Job against this image and,
	// on success, moves it to Image (promote) + clears PendingImage. The
	// chart renders no pod while Image is empty (0 replicas hold), so the
	// un-migrated image never serves. Nil for services with no release
	// hook (Image is set directly) and for built runtimes.
	PendingImage *KusoImage `json:"pendingImage,omitempty"`
```

- [ ] **Step 2: Add the CRD schema** under `spec.properties` in the environments CRD. Find the existing `image:` property (grep `"image:"` in the file) and add a sibling `pendingImage:` with the SAME shape (mirror the `image` block's `properties: {repository, tag, pullPolicy}` exactly):

```yaml
                pendingImage:
                  type: object
                  description: >-
                    A runtime=image target withheld until the service's
                    release hook (migration) succeeds. Promoted to image by
                    the imagerelease watcher.
                  properties:
                    repository:
                      type: string
                    tag:
                      type: string
                    pullPolicy:
                      type: string
```

(Read the existing `image:` block first and copy its exact indentation + property set so pendingImage matches.)

- [ ] **Step 3: Build + CRD parses**

Run: `cd server-go && go build ./...` then `python3 -c "import yaml; yaml.safe_load(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml'))" && echo OK`
Expected: compiles; YAML parses.

- [ ] **Step 4: Assert the schema landed at spec level**

Run:
```bash
python3 -c "import yaml; d=yaml.safe_load(open('operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml')); p=d['spec']['versions'][0]['schema']['openAPIV3Schema']['properties']['spec']['properties']; print('pendingImage' in p, sorted(p.get('pendingImage',{}).get('properties',{}).keys()))"
```
Expected: `True ['pullPolicy', 'repository', 'tag']`

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/kube/types.go operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml
git commit -m "feat(env): pendingImage field for withheld runtime=image release gating"
```

---

## Task 2: Withhold the image in AddService when a release hook exists

**Files:**
- Modify: `server-go/internal/projects/services_ops.go`
- Test: `server-go/internal/projects/image_release_withhold_test.go`

**Interfaces:**
- Consumes: `KusoEnvironmentSpec.PendingImage` (Task 1); `created.Spec.Release`, `created.Spec.Image`.
- Behavior: In AddService's production-env literal (~line 589, `Image: created.Spec.Image`), split by release-hook presence:
  - release hook present AND runtime image AND kind != preview → `Image: nil`, `PendingImage: created.Spec.Image`
  - else → `Image: created.Spec.Image`, `PendingImage: nil` (today's behavior)

- [ ] **Step 1: Write the failing test**

```go
package projects

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

func TestAddService_WithholdsImageWhenReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	img := &ServiceImageSpec{Repository: "ghcr.io/x/app", Tag: "v1"}
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "image", Port: 3000, Image: img,
		Release: &PatchReleaseRequest{Command: []string{"sh", "-c", "migrate"}},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if env.Spec.Image != nil {
		t.Errorf("image must be WITHHELD (nil) when a release hook is present, got %+v", env.Spec.Image)
	}
	want := &kube.KusoImage{Repository: "ghcr.io/x/app", Tag: "v1"}
	if !reflect.DeepEqual(env.Spec.PendingImage, want) {
		t.Errorf("pendingImage: got %+v, want %+v", env.Spec.PendingImage, want)
	}
}

func TestAddService_NoWithholdWithoutReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	img := &ServiceImageSpec{Repository: "ghcr.io/x/app", Tag: "v1"}
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "image", Port: 3000, Image: img,
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, _ := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if env.Spec.Image == nil || env.Spec.Image.Tag != "v1" {
		t.Errorf("image must be live immediately when NO release hook: got %+v", env.Spec.Image)
	}
	if env.Spec.PendingImage != nil {
		t.Errorf("pendingImage must be nil when no release hook: got %+v", env.Spec.PendingImage)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd server-go && go test ./internal/projects/ -run 'TestAddService_Withholds|TestAddService_NoWithhold' -v`
Expected: FAIL (PendingImage always nil, image always set).

- [ ] **Step 3: Implement the withhold** in the AddService env literal. Where the literal currently has `Image: created.Spec.Image,` (~line 589), replace with a computed pair. Before the `env := &kube.KusoEnvironment{...}` construction, compute:

```go
	// Withhold the image when a release hook must run first (runtime=image
	// only — built runtimes start imageless and the build poller promotes
	// after release). The imagerelease watcher runs the migration Job and
	// promotes pendingImage→image on success. Preview envs are excluded
	// (their migrations are owned by the seed path).
	var envImage, envPendingImage *kube.KusoImage
	if created.Spec.Image != nil &&
		created.Spec.Release != nil && len(created.Spec.Release.Command) > 0 &&
		created.Spec.Runtime == "image" {
		envPendingImage = created.Spec.Image
	} else {
		envImage = created.Spec.Image
	}
```

Then in the literal set:
```go
			Image:        envImage,
			PendingImage: envPendingImage,
```
(replacing the single `Image: created.Spec.Image,` line).

- [ ] **Step 4: Run to verify it passes**

Run: `cd server-go && go test ./internal/projects/ -run 'TestAddService_Withholds|TestAddService_NoWithhold|TestAddService_CopiesRelease' -v`
Expected: PASS (withhold when hook present; direct when absent; release still propagates).

- [ ] **Step 5: Full projects package + build**

Run: `cd server-go && go build ./... && go test ./internal/projects/ 2>&1 | tail -3`
Expected: build OK, package tests pass.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/projects/services_ops.go server-go/internal/projects/image_release_withhold_test.go
git commit -m "feat(service): withhold runtime=image image into pendingImage when a release hook exists"
```

---

## Task 3: The imagerelease watcher

**Files:**
- Create: `server-go/internal/imagerelease/watcher.go`
- Test: `server-go/internal/imagerelease/watcher_test.go`

**Interfaces:**
- Consumes: `releaserun.Runner` (via a small interface for testability), `kube.Client`, `KusoEnvironmentSpec.PendingImage`/`Image`/`Release`/`Kind`.
- Produces:
  - `type Runner interface { Run(ctx context.Context, ns string, env *kube.KusoEnvironment, image *kube.KusoImage) (releaserun.Result, error) }`
  - `type Watcher struct { Kube *kube.Client; Namespace string; Logger *slog.Logger; Tick time.Duration; Release Runner; Notify ... }`
  - `func (w *Watcher) Run(ctx context.Context)` — leader-gated tick loop.
  - `func (w *Watcher) reconcileOnce(ctx context.Context) error` — the testable unit: list envs with pendingImage, run release, promote-or-mark-failed.
  - `func (w *Watcher) promote(ctx context.Context, ns, envName string, img *kube.KusoImage) error` — RMW: set Image=img, clear PendingImage.

- [ ] **Step 1: Write the failing test** — a fake Runner + fake kube env, assert promote-on-success and withhold-on-failure:

```go
package imagerelease

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

type fakeRunner struct{ outcome releaserun.Outcome; err error }

func (f fakeRunner) Run(_ context.Context, _ string, _ *kube.KusoEnvironment, _ *kube.KusoImage) (releaserun.Result, error) {
	return releaserun.Result{Outcome: f.outcome, JobName: "j"}, f.err
}

func TestReconcile_PromotesOnSuccess(t *testing.T) {
	// Seed a KusoEnvironment with PendingImage set + a release hook, a fake
	// kube client (mirror how projects/*_test.go builds one — reuse that
	// harness pattern), and a fakeRunner returning OutcomeSucceeded.
	// After reconcileOnce: env.Spec.Image == the pending image, and
	// env.Spec.PendingImage == nil.
	// (Implementer: construct the fake env via the same test scaffolding
	// the imagerelease package can reach; if kube.Client fakes live in
	// another package, add a minimal local fake that supports
	// List/Get/Update of KusoEnvironment.)
	t.Skip("implement with the kube fake harness")
}

func TestReconcile_WithholdsOnFailure(t *testing.T) {
	// fakeRunner returns OutcomeFailed → env.Spec.Image stays nil,
	// PendingImage retained, a notify/mark-failed recorded.
	t.Skip("implement with the kube fake harness")
}
```

(Implementer: replace the `t.Skip` stubs with real tests using whatever kube-fake harness the codebase exposes — check `internal/scaledown/*_test.go` and `internal/projects/*_test.go` for the fake `kube.Client` construction and mirror it. The assertions are the contract: success → image set + pending cleared; failure → image nil + pending retained.)

- [ ] **Step 2: Run to verify it fails/skips**

Run: `cd server-go && go test ./internal/imagerelease/ -v`
Expected: build error (package doesn't exist yet) → after stubbing, skips.

- [ ] **Step 3: Implement the watcher**

```go
// Package imagerelease runs the pre-deploy release hook (migrations) for
// runtime=image services, which skip the build pipeline and so are never
// seen by the build poller's release path. It reconciles KusoEnvironments
// carrying a withheld spec.pendingImage: it runs the release Job against
// that image and, on success, promotes pendingImage→image (the chart then
// scales the held-at-0 pod up onto the migrated image). On failure the
// image stays withheld and the failure is surfaced.
package imagerelease

import (
	"context"
	"log/slog"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

// Runner is the release-Job runner (releaserun.Runner satisfies it).
type Runner interface {
	Run(ctx context.Context, ns string, env *kube.KusoEnvironment, image *kube.KusoImage) (releaserun.Result, error)
}

type Watcher struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger
	Tick      time.Duration
	Release   Runner
	// Notify is optional — a func to surface a release failure (bell/webhook).
	Notify func(project, service, msg string)
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	tick := w.Tick
	if tick <= 0 {
		tick = 15 * time.Second
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.reconcileOnce(ctx); err != nil {
				w.Logger.Error("imagerelease reconcile", "err", err)
			}
		}
	}
}

// reconcileOnce lists envs with a withheld pendingImage + release hook and
// drives each through the release Job → promote/withhold decision.
func (w *Watcher) reconcileOnce(ctx context.Context) error {
	// Single-tenant: all env CRs live in w.Namespace (the kuso namespace).
	envs, err := w.Kube.ListKusoEnvironments(ctx, w.Namespace)
	if err != nil {
		return err
	}
	for i := range envs {
		e := &envs[i]
		if e.Spec.PendingImage == nil {
			continue
		}
		if e.Spec.Release == nil || len(e.Spec.Release.Command) == 0 {
			continue // shouldn't happen (we only set pendingImage with a hook) — skip defensively
		}
		if e.Spec.Kind == "preview" {
			continue
		}
		res, err := w.Release.Run(ctx, w.Namespace, e, e.Spec.PendingImage)
		if err != nil {
			w.Logger.Error("imagerelease: run", "env", e.Name, "err", err)
			continue // transient — retry next tick (Job is idempotent per env,tag)
		}
		switch res.Outcome {
		case releaserun.OutcomeSucceeded:
			if err := w.promote(ctx, w.Namespace, e.Name, e.Spec.PendingImage); err != nil {
				w.Logger.Error("imagerelease: promote", "env", e.Name, "err", err)
				continue
			}
			w.Logger.Info("imagerelease: promoted after release", "env", e.Name, "job", res.JobName)
		default: // Failed / TimedOut
			w.Logger.Warn("imagerelease: release failed, image withheld", "env", e.Name, "outcome", res.Outcome, "job", res.JobName)
			if w.Notify != nil {
				w.Notify(e.Spec.Project, e.Spec.Service, "release hook failed: "+res.Message)
			}
			// Leave pendingImage set. The per-(env,tag) Job name blocks a
			// re-run of the same tag until the user changes the image.
		}
	}
	return nil
}

// promote sets Image=img and clears PendingImage via read-modify-write with
// retry (mirrors the build poller's promoteEnvImageCAS conflict handling).
func (w *Watcher) promote(ctx context.Context, ns, envName string, img *kube.KusoImage) error {
	_, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(env *kube.KusoEnvironment) error {
		env.Spec.Image = img
		env.Spec.PendingImage = nil
		return nil
	})
	return err
}
```

Confirmed kube client API (already verified against `internal/kube/crds.go`):
- `ListKusoEnvironments(ctx, namespace string) ([]KusoEnvironment, error)` (crds.go:118).
- `UpdateKusoEnvironmentWithRetry(ctx, namespace, name string, mutate func(*KusoEnvironment) error) (*KusoEnvironment, error)` (crds.go:340) — returns the updated env + error; discard the env, keep the error.
- Envs live in the single `kuso` namespace (`w.Namespace`); no per-env ns resolution needed for a single-tenant install.

- [ ] **Step 4: Implement the tests** (replace the skips) using the kube-fake harness; assert promote-on-success + withhold-on-failure.

- [ ] **Step 5: Run + build**

Run: `cd server-go && go build ./... && go test ./internal/imagerelease/ -v && go vet ./internal/imagerelease/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/imagerelease/
git commit -m "feat(imagerelease): watcher runs release hook + promotes image for runtime=image"
```

---

## Task 4: Wire the watcher into main.go

**Files:**
- Modify: `server-go/cmd/kuso-server/main.go`

**Interfaces:**
- Consumes: `imagerelease.Watcher` (Task 3); `releaserun.New(kc)`.

- [ ] **Step 1: Wire it into `startSingletons`** (the leader-gated closure — find where `scaledown`/`sampler` are `go X.Run(workCtx)`). Add:

```go
	go (&imagerelease.Watcher{
		Kube:      kc,
		Namespace: *namespace,
		Logger:    logger,
		Release:   releaserun.New(kc),
		Notify:    func(project, service, msg string) { /* wire to notifyDisp if in scope, else omit */ },
	}).Run(workCtx)
```

(Match how the sibling watchers are constructed + launched in that block. If `notifyDisp` is in scope there, wire Notify to it using the same event shape `markReleaseFailed` uses; if not, leave Notify nil — the log + withheld image are the load-bearing behavior, the notify is a nice-to-have.)

Add the import `"kuso/server/internal/imagerelease"`.

- [ ] **Step 2: Build**

Run: `cd server-go && go build ./...`
Expected: compiles.

- [ ] **Step 3: Commit**

```bash
git add server-go/cmd/kuso-server/main.go
git commit -m "feat(server): run the imagerelease watcher under the singleton leader gate"
```

---

## Task 5: Ship, apply CRD, live-verify plausible + regression

**Files:**
- Modify: `docs/AGENT_SMOKE_TEST.md`

- [ ] **Step 1: Build artifacts**

Run: `cd /Users/sisle/code/work/kuso && (cd web && npm run build) && (cd server-go && go build ./...) && (cd cli && go build -o /tmp/kuso ./cmd)`

- [ ] **Step 2: Ship** (real; the flag is `--dry-run`, not `DRY_RUN=1`):

Run: `make ship VERSION=v0.18.112` (bump if a later version already shipped)

- [ ] **Step 3: Apply the CRD schema change** (auto-updater only flips image tags):

```bash
scp -i ~/.ssh/keys/hetzner operator/config/crd/bases/application.kuso.sislelabs.com_kusoenvironments.yaml root@kuso.sislelabs.com:/tmp/
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl apply -f /tmp/application.kuso.sislelabs.com_kusoenvironments.yaml"
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso-operator-system rollout restart deploy/kuso-operator-controller-manager"
```

- [ ] **Step 4: Roll the server** to the new version:

```bash
./dist/kuso-darwin-arm64 upgrade --version v0.18.112
ssh -i ~/.ssh/keys/hetzner root@kuso.sislelabs.com "kubectl -n kuso rollout status deploy/kuso-server --timeout=90s"
```

- [ ] **Step 5: Verify plausible end-to-end**

```bash
/tmp/kuso marketplace deploy plausible --project ir-verify --set host=ir-verify.sislelabs.com
```
Then confirm, allowing time:
- `kubectl -n kuso get kusoenvironment ir-verify-plausible-production -o jsonpath='{.spec.pendingImage}{"|"}{.spec.image}'` → initially pendingImage set, image empty.
- A release Job `ir-verify-plausible-production-release-<tag>` appears and completes.
- After success: `.spec.image` is set, `.spec.pendingImage` cleared.
- `kubectl -n kuso rollout status deploy/ir-verify-plausible-production` → Ready; NO "relation ... does not exist" in logs.
- `curl -I https://ir-verify.sislelabs.com` → 200/302.

- [ ] **Step 6: Regression — the no-hook apps still deploy directly**

```bash
/tmp/kuso marketplace deploy n8n --project ir-n8n --set host=ir-n8n.sislelabs.com
```
Confirm n8n (no release hook) gets `.spec.image` set immediately (no pendingImage, no release Job) and reaches Ready — proving the withhold path doesn't touch hook-less services.

- [ ] **Step 7: Teardown + doc**

Delete `ir-verify` + `ir-n8n` projects (and reclaim their PVCs). Add the image-path release smoke to `docs/AGENT_SMOKE_TEST.md`. Commit the doc.

---

## Self-Review Notes

- **Spec coverage:** pendingImage field+CRD (T1), withhold-on-create (T2), watcher run+promote+withhold (T3), wiring (T4), ship+CRD+live-verify+regression (T5). Safety property (no change without a hook) asserted in T2 both tests + T6 regression.
- **Deferred/known:** image-UPDATE path (changing the tag on an existing image service with a hook) — T2 covers create; the update path (services_ops.go:2078) has the same withhold need. FLAGGED: add the same withhold branch there. If the marketplace only ever creates (never patches image), create-path is enough for v1, but the update path should get the same treatment — call it out in T2 as a follow-up if not done inline. Implementer: if the image-patch path is short, apply the identical withhold there in T2 and add a patch-path test; otherwise note it.
- **Placeholder scan:** T3 has explicit "verify the real kube method name" notes (ListAllKusoEnvironments / NamespaceForEnv / UpdateKusoEnvironmentWithRetry are named as placeholders to confirm) — these are inspect-before-use directives with the concrete grep target, not vague TODOs. The test stubs in T3 step 1 are explicitly replaced in step 4.
- **Type consistency:** `PendingImage *KusoImage` used in T1/T2/T3. `Runner` interface in T3 matches `releaserun.Runner.Run` signature. Promote clears pending + sets image consistently.
