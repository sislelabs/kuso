# Kuso envFromSecrets Per-Service & Per-Env Drop Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop kuso's addon `envFromSecrets` fan-out from dropping the per-service (`<project>-<service>-secrets`) and per-env (`<project>-<service>-<env>-secrets`) secrets, so every service's pods keep their service- and env-scoped secrets (auth tokens, env overrides) after any addon operation.

**Architecture:** Add `kube.ServiceSecretName` / `kube.EnvSecretName` name helpers (joining `kube.SharedSecretNames` from the prior fix), refactor `secrets.Name` to delegate to them (one implementation of the naming logic), and rewrite `addons.RefreshEnvSecrets`'s env loop to build a per-env `envFromSecrets` list that includes that env's per-service and per-env secret, derived from the labels each env CR already carries. No CRD, chart, or operator change.

**Tech Stack:** Go (server-go, one `go.work` module). Tests via `go test`.

**Verification model:** `go test` from `server-go/`. The existing `RefreshEnvSecrets` tests (`TestAdd_RefreshesEnvSecrets`, `TestDelete_RefreshesEnvSecrets`, `TestRefreshEnvSecrets_KeepsSharedSecrets`) will need their expectations widened because the fixed fan-out adds per-service + per-env entries.

---

## File Structure

Files modified:
- `server-go/internal/kube/selectors.go` — add `ServiceSecretName`, `EnvSecretName`, and the env-name sanitizer; add `regexp`/`strings` imports.
- `server-go/internal/kube/selectors_test.go` — unit tests for the two new helpers.
- `server-go/internal/secrets/secrets.go` — refactor `Name` to delegate to the `kube` helpers; remove the now-dead `envSafeRE` if nothing else uses it.
- `server-go/internal/addons/addons.go` — rewrite `RefreshEnvSecrets`'s env loop to build a per-env secret list.
- `server-go/internal/addons/addons_test.go` — widen the three existing `RefreshEnvSecrets` test expectations; add a per-service/per-env regression test.

Files unchanged: CRDs, all Helm charts, the operator, `dist/`.

---

## Task 1: Add `ServiceSecretName` + `EnvSecretName` helpers to `kube`

**Files:**
- Modify: `server-go/internal/kube/selectors.go`

- [ ] **Step 1: Confirm baseline build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds. If it fails, STOP and report BLOCKED.

- [ ] **Step 2: Add `regexp` to the imports**

In `server-go/internal/kube/selectors.go`, the import block currently is:
```go
import (
	"k8s.io/apimachinery/pkg/labels"
)
```
Replace it with:
```go
import (
	"regexp"
	"strings"

	"k8s.io/apimachinery/pkg/labels"
)
```

- [ ] **Step 3: Add the sanitizer regex and the two helpers**

In `server-go/internal/kube/selectors.go`, append at the END of the file (after `SharedSecretNames`):

```go

// envSecretNameRE strips characters that aren't valid in a Kubernetes
// resource-name segment, so an env name can be interpolated into a
// Secret name safely. Matches secrets.Name's historical sanitization.
var envSecretNameRE = regexp.MustCompile(`[^a-z0-9-]`)

// ServiceSecretName returns the service-scoped shared secret name:
// <project>-<service>-secrets. This Secret holds keys set via
// `kuso secret set <project> <service> KEY VALUE` with no --env scope.
// Like the project-shared secret it is marked optional:true by the
// kusoenvironment chart, so referencing it before it exists is safe.
func ServiceSecretName(project, service string) string {
	return project + "-" + service + "-secrets"
}

// EnvSecretName returns the env-scoped secret name:
// <project>-<service>-<sanitized-env>-secrets. The env name is
// lowercased and any character outside [a-z0-9-] becomes "-" so the
// result is a valid Kubernetes resource-name segment. Holds keys set
// at a specific env scope (e.g. preview-PR overrides).
func EnvSecretName(project, service, env string) string {
	safe := envSecretNameRE.ReplaceAllString(strings.ToLower(env), "-")
	return project + "-" + service + "-" + safe + "-secrets"
}
```

- [ ] **Step 4: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/kube/selectors.go
git commit -m "feat(kube): add ServiceSecretName and EnvSecretName helpers"
```

---

## Task 2: Unit-test the new `kube` helpers

**Files:**
- Modify: `server-go/internal/kube/selectors_test.go`

- [ ] **Step 1: Confirm the test file exists**

Run (from repo root): `ls server-go/internal/kube/selectors_test.go`
It should exist (created by the prior fix; it has `TestSharedSecretNames`). If it does not exist, create it with `package kube` and `import "testing"`.

- [ ] **Step 2: Append the helper tests**

Append these two functions to `server-go/internal/kube/selectors_test.go`:

```go
func TestServiceSecretName(t *testing.T) {
	got := ServiceSecretName("alpha", "web")
	if got != "alpha-web-secrets" {
		t.Errorf("ServiceSecretName = %q, want %q", got, "alpha-web-secrets")
	}
}

func TestEnvSecretName(t *testing.T) {
	cases := []struct {
		project, service, env, want string
	}{
		{"alpha", "web", "production", "alpha-web-production-secrets"},
		// Mixed case + punctuation must be lowercased and sanitized to
		// [a-z0-9-] so the result is a valid resource-name segment.
		{"alpha", "web", "preview/PR-7", "alpha-web-preview-pr-7-secrets"},
		{"alpha", "api", "Staging Env", "alpha-api-staging-env-secrets"},
	}
	for _, c := range cases {
		got := EnvSecretName(c.project, c.service, c.env)
		if got != c.want {
			t.Errorf("EnvSecretName(%q,%q,%q) = %q, want %q",
				c.project, c.service, c.env, got, c.want)
		}
	}
}
```

- [ ] **Step 3: Run the tests**

Run (from `server-go/`): `go test ./internal/kube/ -run "ServiceSecretName|EnvSecretName" -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add server-go/internal/kube/selectors_test.go
git commit -m "test(kube): cover ServiceSecretName and EnvSecretName"
```

---

## Task 3: Refactor `secrets.Name` to delegate to the `kube` helpers

**Files:**
- Modify: `server-go/internal/secrets/secrets.go`

This gives the naming logic one implementation so the `kube` helpers and `secrets.Name` cannot drift.

- [ ] **Step 1: Replace the `Name` function body**

In `server-go/internal/secrets/secrets.go`, the current code is:
```go
// envSafeRE strips characters that aren't valid in a k8s resource name
// segment so we can interpolate env names into Secret names safely.
var envSafeRE = regexp.MustCompile(`[^a-z0-9-]`)

// Name returns the per-scope Secret name. env=="" produces the shared
// name, otherwise the env name is sanitised before appending.
func Name(project, service, env string) string {
	base := fmt.Sprintf("%s-%s", project, service)
	if env == "" {
		return base + "-secrets"
	}
	safe := envSafeRE.ReplaceAllString(strings.ToLower(env), "-")
	return fmt.Sprintf("%s-%s-secrets", base, safe)
}
```

Replace BOTH the `envSafeRE` var and the `Name` function with:
```go
// Name returns the per-scope Secret name. env=="" produces the
// service-scoped shared name, otherwise the env-scoped name. Delegates
// to the kube package so the naming + env-name sanitization has a
// single implementation shared with addons.RefreshEnvSecrets.
func Name(project, service, env string) string {
	if env == "" {
		return kube.ServiceSecretName(project, service)
	}
	return kube.EnvSecretName(project, service, env)
}
```

- [ ] **Step 2: Fix imports**

Removing `envSafeRE` may leave `regexp` unused in `secrets.go`. The `Name` rewrite may also leave `fmt` and/or `strings` unused IF they are used nowhere else in the file. Run (from `server-go/`): `go build ./internal/secrets/` — the compiler will name any now-unused import. Remove ONLY the imports the compiler flags as unused; leave the rest. Do not remove `kube` (now used by `Name`) — confirm `"kuso/server/internal/kube"` is still imported (it already was).

- [ ] **Step 3: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 4: Run the secrets package tests**

Run (from `server-go/`): `go test ./internal/secrets/`
Expected: PASS — `Name` produces the same strings as before (delegation is behavior-preserving). If a test fails, the delegation produced a different string than the old code — investigate (it should not happen; the `kube` helpers reproduce the exact logic).

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/secrets/secrets.go
git commit -m "refactor(secrets): delegate Name to kube secret-name helpers"
```

---

## Task 4: Fix `RefreshEnvSecrets` — per-env secret list

**Files:**
- Modify: `server-go/internal/addons/addons.go`

This is the core bug fix.

- [ ] **Step 1: Read the current function**

In `server-go/internal/addons/addons.go`, find `func (s *Service) RefreshEnvSecrets`. It currently builds one `secrets` slice (addon-conn-secrets + `kube.SharedSecretNames(project)`), lists envs, and in the loop calls `buildEnvFromSecretsPatch(secrets)` with that SAME slice for every env. The loop body is currently:

```go
	for i := range envs {
		envName := envs[i].Name
		patch := buildEnvFromSecretsPatch(secrets)
		if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, envName, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("patch env %s: %w", envName, err)
		}
	}
```

- [ ] **Step 2: Rename the project-wide slice to `baseSecrets`**

The slice currently named `secrets` (built from addon-conn-secrets + `kube.SharedSecretNames(project)`) is now the project-wide *base*. Rename the variable from `secrets` to `baseSecrets` at its declaration and at the `append` that adds `kube.SharedSecretNames(project)...`. After this rename, `secrets` is no longer referenced anywhere — the loop will build per-env slices.

- [ ] **Step 3: Add `slices` to the imports**

In `server-go/internal/addons/addons.go`, add `"slices"` to the standard-library import group (alongside `"context"`, `"encoding/json"`, `"errors"`, `"fmt"`, `"regexp"`, `"strings"`). Keep alphabetical order: `"slices"` sorts after `"regexp"` and before `"strings"`.

- [ ] **Step 4: Rewrite the loop to build a per-env list**

Replace the loop body (the `for i := range envs { ... }` block shown in Step 1) with:

```go
	for i := range envs {
		env := &envs[i]
		// Start from the project-wide base, then add this env's
		// service- and env-scoped secrets. Clone so each env gets an
		// independent slice (append on a shared backing array would
		// cross-contaminate). The merge-patch REPLACES envFromSecrets
		// wholesale, so the per-env list must be complete.
		perEnv := slices.Clone(baseSecrets)
		// The short service name + env name live on labels every
		// kuso-created env CR carries. A hand-created CR missing the
		// service label degrades gracefully: it still gets the base
		// (addon-conn + project-shared) secrets, just not its own
		// service/env-scoped ones.
		svc := env.Labels[kube.LabelService]
		if svc != "" {
			perEnv = append(perEnv, kube.ServiceSecretName(project, svc))
			if envName := env.Labels[kube.LabelEnv]; envName != "" {
				perEnv = append(perEnv, kube.EnvSecretName(project, svc, envName))
			}
		}
		patch := buildEnvFromSecretsPatch(perEnv)
		if _, err := s.Kube.Dynamic.Resource(kube.GVREnvironments).Namespace(ns).
			Patch(ctx, env.Name, types.MergePatchType, patch, metav1.PatchOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("patch env %s: %w", env.Name, err)
		}
	}
```

Note: the previous loop used a local `envName := envs[i].Name` (the env CR's *resource* name) for the error message; the rewrite uses `env.Name` for that and introduces a *separate* `envName` (the env *label* value) inside the `if svc != ""` block. These are different values with the same identifier in different scopes — the inner `envName` shadows nothing because the outer one no longer exists. This is intentional and correct.

- [ ] **Step 5: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds. If `slices` is reported unused, the loop rewrite didn't land — recheck Step 4.

- [ ] **Step 6: Run the addons tests — the three RefreshEnvSecrets tests are EXPECTED to FAIL**

Run (from `server-go/`): `go test ./internal/addons/ -run "RefreshEnvSecrets|RefreshesEnvSecrets" -v`
Expected: `TestAdd_RefreshesEnvSecrets`, `TestDelete_RefreshesEnvSecrets`, and `TestRefreshEnvSecrets_KeepsSharedSecrets` now FAIL — the fixed fan-out adds per-service + per-env entries that the current assertions don't expect. This is EXPECTED; Task 5 updates them. Do not revert the fix.

- [ ] **Step 7: Commit the fix**

```bash
git add server-go/internal/addons/addons.go
git commit -m "fix(addons): keep per-service and per-env secrets in envFromSecrets fan-out"
```

---

## Task 5: Update the addon fan-out tests

**Files:**
- Modify: `server-go/internal/addons/addons_test.go`

CONTEXT: `seedEnv(project, service, kind, name)` seeds a `KusoEnvironment` with labels `kuso.sislelabs.com/project=<project>`, `kuso.sislelabs.com/service=<service>`, `kuso.sislelabs.com/env=<kind>`. The existing tests call `seedEnv("alpha", "web", "production", "alpha-web-production")`. So after the fix, that env's `envFromSecrets` gains `alpha-web-secrets` (per-service) and `alpha-web-production-secrets` (per-env), on top of the addon-conn + shared secrets.

- [ ] **Step 1: Fix `TestAdd_RefreshesEnvSecrets`**

Find `TestAdd_RefreshesEnvSecrets`. After the prior fix it asserts:
```go
	wantSecrets := []string{"alpha-pg-conn", "alpha-shared", "kuso-instance-shared"}
	if len(envCR.Spec.EnvFromSecrets) != len(wantSecrets) {
		t.Fatalf("envFromSecrets = %+v, want %+v", envCR.Spec.EnvFromSecrets, wantSecrets)
	}
	for i, want := range wantSecrets {
		if envCR.Spec.EnvFromSecrets[i] != want {
			t.Errorf("envFromSecrets[%d] = %q, want %q", i, envCR.Spec.EnvFromSecrets[i], want)
		}
	}
```
Replace that block with an order-independent membership assertion (the fan-out appends conn-secrets, then shared, then per-service, then per-env — but membership is the meaningful contract, and order-independence makes the test robust to future ordering tweaks):
```go
	want := []string{
		"alpha-pg-conn",
		"alpha-shared",
		"kuso-instance-shared",
		"alpha-web-secrets",
		"alpha-web-production-secrets",
	}
	got := envCR.Spec.EnvFromSecrets
	if len(got) != len(want) {
		t.Fatalf("envFromSecrets = %+v, want exactly %+v", got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("envFromSecrets %+v missing %q", got, w)
		}
	}
```

- [ ] **Step 2: Fix `TestDelete_RefreshesEnvSecrets`**

Find `TestDelete_RefreshesEnvSecrets`. After the prior fix it asserts a 3-entry `wantSecrets` starting `alpha-redis-conn`. After deleting the `pg` addon, `redis` remains, and the `alpha-web-production` env also gets its per-service + per-env secrets. Replace its assertion block (the `wantSecrets := []string{"alpha-redis-conn", ...}` block) with:
```go
	want := []string{
		"alpha-redis-conn",
		"alpha-shared",
		"kuso-instance-shared",
		"alpha-web-secrets",
		"alpha-web-production-secrets",
	}
	got := envCR.Spec.EnvFromSecrets
	if len(got) != len(want) {
		t.Fatalf("envFromSecrets after delete = %+v, want exactly %+v", got, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("envFromSecrets after delete %+v missing %q", got, w)
		}
	}
```

- [ ] **Step 3: Fix `TestRefreshEnvSecrets_KeepsSharedSecrets`**

Find `TestRefreshEnvSecrets_KeepsSharedSecrets` (added by the prior fix). It uses a `has(name)` membership helper and checks `["alpha-pg-conn", "alpha-shared", "kuso-instance-shared"]`. Widen the checked list to include the per-service + per-env secrets — change the `for _, want := range []string{...}` line to:
```go
	for _, want := range []string{
		"alpha-pg-conn",
		"alpha-shared",
		"kuso-instance-shared",
		"alpha-web-secrets",
		"alpha-web-production-secrets",
	} {
```
(Leave the rest of that test — the `has` closure and the loop body — unchanged.)

- [ ] **Step 4: Add a per-service isolation regression test**

Immediately after `TestRefreshEnvSecrets_KeepsSharedSecrets`, add a test proving two services in the same project each get their OWN per-service secret (no cross-contamination):

```go
func TestRefreshEnvSecrets_PerServiceIsolation(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
		seedEnv("alpha", "api", "production", "alpha-api-production"),
	)

	// Adding an addon triggers RefreshEnvSecrets across every env.
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	has := func(list []string, name string) bool {
		for _, s := range list {
			if s == name {
				return true
			}
		}
		return false
	}

	webEnv, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get web env: %v", err)
	}
	apiEnv, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-production")
	if err != nil {
		t.Fatalf("get api env: %v", err)
	}

	// Each env carries ITS OWN service secret, and not the sibling's.
	if !has(webEnv.Spec.EnvFromSecrets, "alpha-web-secrets") {
		t.Errorf("web env missing alpha-web-secrets: %+v", webEnv.Spec.EnvFromSecrets)
	}
	if has(webEnv.Spec.EnvFromSecrets, "alpha-api-secrets") {
		t.Errorf("web env wrongly has alpha-api-secrets: %+v", webEnv.Spec.EnvFromSecrets)
	}
	if !has(apiEnv.Spec.EnvFromSecrets, "alpha-api-secrets") {
		t.Errorf("api env missing alpha-api-secrets: %+v", apiEnv.Spec.EnvFromSecrets)
	}
	if has(apiEnv.Spec.EnvFromSecrets, "alpha-web-secrets") {
		t.Errorf("api env wrongly has alpha-web-secrets: %+v", apiEnv.Spec.EnvFromSecrets)
	}
}
```

- [ ] **Step 5: Run the addons tests clean**

Run (from `server-go/`): `go test ./internal/addons/`
Expected: PASS — all addon tests including the three updated and the new isolation test. If `TestRefreshEnvSecrets_PerServiceIsolation` fails to compile because a helper signature differs, read how `TestRefreshEnvSecrets_KeepsSharedSecrets` uses `fakeService`/`seedEnv`/`GetKusoEnvironment` and match it.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/addons/addons_test.go
git commit -m "test(addons): assert fan-out keeps per-service and per-env secrets"
```

---

## Task 6: Full test sweep

**Files:** none (verification only)

- [ ] **Step 1: Build the workspace**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 2: Full server-go test suite**

Run (from `server-go/`): `go test ./...`
Expected: PASS. If a test fails: determine whether it is pre-existing/unrelated (flaky, needs a live cluster) or caused by this change. Report which. Fix only failures caused by this change.

- [ ] **Step 3: Confirm `secrets.Name` and the `kube` helpers agree**

Run (from `server-go/`):
```bash
go test ./internal/secrets/ ./internal/kube/ -run "Name|SecretName" -v
```
Expected: PASS — `secrets.Name` (now delegating) and the `kube` helpers both green.

- [ ] **Step 4: Confirm the fan-out uses the helpers**

Run (from repo root):
```bash
grep -n "ServiceSecretName\|EnvSecretName\|SharedSecretNames" server-go/internal/addons/addons.go
```
Expected: `RefreshEnvSecrets` references `SharedSecretNames`, `ServiceSecretName`, and `EnvSecretName`.

- [ ] **Step 5: Final commit (only if cleanup was needed)**

```bash
git add -A server-go
git commit -m "chore: envFromSecrets per-service fix verification cleanup"
```
If nothing changed, skip.

---

## Rollout (post-merge — performed manually, not part of subagent execution)

After this plan is merged:
1. Cut a patch release: `KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh v0.13.12` (commit any `git-cliff` CHANGELOG.archive.md change first so the tree is clean).
2. `kuso upgrade --version v0.13.12` to roll the cluster.
3. distill's env CRs were manually patched to include `distill-<service>-secrets`; the fix ensures the next addon operation's `RefreshEnvSecrets` will no longer drop them. No further distill action needed.

---

## Self-Review Notes

- **Spec coverage:** Spec §Change 1 (kube helpers + `secrets.Name` delegation) → Tasks 1, 2, 3. §Change 2 (fix `RefreshEnvSecrets`) → Task 4 (+ tests Task 5). §Testing (helper tests, `secrets.Name` test, `RefreshEnvSecrets` test incl. per-service isolation) → Tasks 2, 3 Step 4, 5. §Rollout → the post-merge section. §Risks: per-env patch (Task 4 builds per-env slices), sanitization drift (Task 3 delegation — one implementation), label trust (Task 4 Step 4 graceful skip when `LabelService` absent).
- **Placeholder scan:** every code step shows exact code. Task 3 Step 2 says "remove only the imports the compiler flags" — that is a deliberate compiler-driven instruction (which imports go unused depends on the rest of the file, which must be read), not a vague placeholder; the action is concrete and verifiable.
- **Type consistency:** `kube.ServiceSecretName(project, service string) string` and `kube.EnvSecretName(project, service, env string) string` are defined in Task 1, called identically in Task 3 (`secrets.Name`) and Task 4 (`RefreshEnvSecrets`). The env label is read as `env.Labels[kube.LabelEnv]` and passed to `EnvSecretName` — consistent with `seedEnv` seeding `kuso.sislelabs.com/env`. Test expectations in Task 5 (`alpha-web-secrets`, `alpha-web-production-secrets`) match `ServiceSecretName("alpha","web")` and `EnvSecretName("alpha","web","production")`.
