# Kuso envFromSecrets Shared-Secret Drop Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop kuso's addon `envFromSecrets` fan-out from silently dropping the project-shared and instance-shared secrets, so every service's pods keep their shared secrets (auth tokens, Stripe keys, Discord bot tokens) after any addon operation.

**Architecture:** Add one shared helper `kube.SharedSecretNames(project)` returning the two always-present shared-secret entries. Make `addons.RefreshEnvSecrets` (the buggy fan-out) append it, and route the two already-correct env-creation sites through the same helper so the invariant is single-sourced. No CRD, chart, or operator change — pure server-side Go logic.

**Tech Stack:** Go (server-go, one `go.work` module). Tests via `go test`.

**Verification model:** `go test` from `server-go/`. The two existing tests `TestAdd_RefreshesEnvSecrets` and `TestDelete_RefreshesEnvSecrets` will need their assertions updated because the fixed fan-out adds two more entries.

---

## File Structure

Files modified:
- `server-go/internal/kube/selectors.go` — add the `SharedSecretNames` helper (package `kube`, where cross-package naming constants already live).
- `server-go/internal/addons/addons.go` — `RefreshEnvSecrets` appends the shared secrets (the bug fix).
- `server-go/internal/projects/services_ops.go` — replace two inline literals with the helper call.
- `server-go/internal/projects/env_groups.go` — replace one inline literal with the helper call.
- `server-go/internal/addons/addons_test.go` — update `TestAdd_RefreshesEnvSecrets` and `TestDelete_RefreshesEnvSecrets` assertions; add a fan-out-keeps-shared-secrets test.
- `server-go/internal/kube/selectors_test.go` — create (or extend if it exists) with a `SharedSecretNames` unit test.

Files unchanged: CRDs, all Helm charts, the operator, `dist/`.

---

## Task 1: Add the `SharedSecretNames` helper

**Files:**
- Modify: `server-go/internal/kube/selectors.go`

- [ ] **Step 1: Confirm baseline build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds. If it fails, STOP and report BLOCKED.

- [ ] **Step 2: Add the helper**

In `server-go/internal/kube/selectors.go`, append at the end of the file (after the existing label-constant block — the file is package `kube`):

```go

// SharedSecretNames returns the two always-present shared-secret
// entries every KusoEnvironment's spec.envFromSecrets must carry: the
// project-shared secret (<project>-shared) and the instance-shared
// secret (kuso-instance-shared). Both are marked optional:true by the
// kusoenvironment Helm chart, so a pod boots cleanly even when the
// Secret has not been created yet.
//
// Single source of truth: addons.RefreshEnvSecrets and the two env-CR
// creation paths in the projects package all build envFromSecrets by
// appending this — so the three sites cannot drift and silently drop
// shared secrets (which is exactly the bug this helper fixes).
func SharedSecretNames(project string) []string {
	return []string{project + "-shared", "kuso-instance-shared"}
}
```

- [ ] **Step 3: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 4: Commit**

```bash
git add server-go/internal/kube/selectors.go
git commit -m "feat(kube): add SharedSecretNames helper for envFromSecrets"
```

---

## Task 2: Unit test for `SharedSecretNames`

**Files:**
- Create or modify: `server-go/internal/kube/selectors_test.go`

- [ ] **Step 1: Check whether the test file exists**

Run (from repo root): `ls server-go/internal/kube/selectors_test.go 2>/dev/null || echo "does not exist"`

- [ ] **Step 2: Write the test**

If `selectors_test.go` does NOT exist, create it with:

```go
package kube

import "testing"

func TestSharedSecretNames(t *testing.T) {
	got := SharedSecretNames("alpha")
	want := []string{"alpha-shared", "kuso-instance-shared"}
	if len(got) != len(want) {
		t.Fatalf("SharedSecretNames len = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SharedSecretNames[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
```

If `selectors_test.go` already exists, append just the `TestSharedSecretNames` function (it is `package kube`, so no import changes beyond ensuring `testing` is imported).

- [ ] **Step 3: Run the test**

Run (from `server-go/`): `go test ./internal/kube/ -run TestSharedSecretNames -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add server-go/internal/kube/selectors_test.go
git commit -m "test(kube): cover SharedSecretNames"
```

---

## Task 3: Fix `RefreshEnvSecrets` — append the shared secrets

**Files:**
- Modify: `server-go/internal/addons/addons.go`

This is the core bug fix.

- [ ] **Step 1: Locate the bug**

In `server-go/internal/addons/addons.go`, find `func (s *Service) RefreshEnvSecrets`. Its body builds `secrets` from addon connection secrets:

```go
	secrets := make([]string, 0, len(addons))
	for _, a := range addons {
		secrets = append(secrets, connSecretName(a.Name))
	}
```

Then later lists envs and calls `buildEnvFromSecretsPatch(secrets)` per env.

- [ ] **Step 2: Append the shared secrets**

Immediately AFTER the `for _, a := range addons` loop that builds `secrets` (i.e. right after the loop's closing `}`, before the `ns := s.nsFor(...)` line), add:

```go
	// Always carry the project-shared + instance-shared secrets. The
	// merge-patch below REPLACES spec.envFromSecrets wholesale, so any
	// entry not in this slice is dropped — omitting the shared secrets
	// here is the bug that silently stripped auth tokens, Stripe keys
	// and Discord bot tokens from every service's pods after an addon
	// add/remove.
	secrets = append(secrets, kube.SharedSecretNames(project)...)
```

The `kube` package is already imported in `addons.go` (used elsewhere as `kube.GVREnvironments` etc.), so no new import is needed. `project` is the function parameter of `RefreshEnvSecrets` — confirm the parameter name by reading the function signature; if it is named differently, use that name.

- [ ] **Step 3: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 4: Run the addons tests — expect the two existing ones to FAIL**

Run (from `server-go/`): `go test ./internal/addons/ -run RefreshEnvSecrets -v`
Expected: `TestAdd_RefreshesEnvSecrets` and `TestDelete_RefreshesEnvSecrets` now FAIL — they assert `len(EnvFromSecrets) != 1`, but the fixed fan-out produces 3 entries. This failure is EXPECTED and confirms the fix changed behavior. Task 4 updates these tests. Do not "fix" the failure by reverting.

- [ ] **Step 5: Commit the fix**

```bash
git add server-go/internal/addons/addons.go
git commit -m "fix(addons): keep shared secrets in envFromSecrets fan-out"
```

---

## Task 4: Update the addon fan-out tests

**Files:**
- Modify: `server-go/internal/addons/addons_test.go`

- [ ] **Step 1: Fix `TestAdd_RefreshesEnvSecrets`**

In `server-go/internal/addons/addons_test.go`, find `TestAdd_RefreshesEnvSecrets`. It currently asserts:

```go
	if len(envCR.Spec.EnvFromSecrets) != 1 || envCR.Spec.EnvFromSecrets[0] != "alpha-pg-conn" {
		t.Errorf("envFromSecrets not patched: %+v", envCR.Spec.EnvFromSecrets)
	}
```

Replace that `if` block with an assertion that the list contains the addon conn-secret AND both shared secrets (order: the loop appends conn-secrets first, then `SharedSecretNames`, so for one addon the slice is `["alpha-pg-conn", "alpha-shared", "kuso-instance-shared"]`):

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

- [ ] **Step 2: Fix `TestDelete_RefreshesEnvSecrets`**

In the same file, find `TestDelete_RefreshesEnvSecrets`. It currently asserts:

```go
	if len(envCR.Spec.EnvFromSecrets) != 1 || envCR.Spec.EnvFromSecrets[0] != "alpha-redis-conn" {
		t.Errorf("envFromSecrets after delete: %+v", envCR.Spec.EnvFromSecrets)
	}
```

After deleting the `pg` addon, `redis` remains, so the list is `["alpha-redis-conn", "alpha-shared", "kuso-instance-shared"]`. Replace that `if` block with:

```go
	wantSecrets := []string{"alpha-redis-conn", "alpha-shared", "kuso-instance-shared"}
	if len(envCR.Spec.EnvFromSecrets) != len(wantSecrets) {
		t.Fatalf("envFromSecrets after delete = %+v, want %+v", envCR.Spec.EnvFromSecrets, wantSecrets)
	}
	for i, want := range wantSecrets {
		if envCR.Spec.EnvFromSecrets[i] != want {
			t.Errorf("envFromSecrets[%d] after delete = %q, want %q", i, envCR.Spec.EnvFromSecrets[i], want)
		}
	}
```

- [ ] **Step 3: Add a focused regression test**

In the same file, immediately after `TestDelete_RefreshesEnvSecrets`, add a test that pins the exact bug — the fan-out must keep the shared secrets even when the project starts with a shared secret already attached to the env CR. Use the same `fakeService` / `seedProj` / `seedEnv` helpers the other tests use:

```go
func TestRefreshEnvSecrets_KeepsSharedSecrets(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
	)

	// Adding an addon triggers RefreshEnvSecrets.
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}

	// The fan-out must keep BOTH shared-secret entries, not just the
	// addon conn-secret. This is the regression guard for the bug
	// where envFromSecrets was replaced with addon-conn-secrets only.
	has := func(name string) bool {
		for _, s := range envCR.Spec.EnvFromSecrets {
			if s == name {
				return true
			}
		}
		return false
	}
	for _, want := range []string{"alpha-pg-conn", "alpha-shared", "kuso-instance-shared"} {
		if !has(want) {
			t.Errorf("envFromSecrets missing %q: %+v", want, envCR.Spec.EnvFromSecrets)
		}
	}
}
```

- [ ] **Step 4: Run the addons tests clean**

Run (from `server-go/`): `go test ./internal/addons/`
Expected: PASS — all addon tests, including the two updated ones and the new regression test.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/addons/addons_test.go
git commit -m "test(addons): assert fan-out keeps shared secrets in envFromSecrets"
```

---

## Task 5: Route the env-creation sites through the helper

**Files:**
- Modify: `server-go/internal/projects/services_ops.go`
- Modify: `server-go/internal/projects/env_groups.go`

These two paths are already correct (they append `project+"-shared"` and `"kuso-instance-shared"` inline). Routing them through `kube.SharedSecretNames` is a behavior-preserving DRY change so the three sites cannot drift again.

- [ ] **Step 1: `services_ops.go` — first site**

In `server-go/internal/projects/services_ops.go`, find the line:

```go
	envFromSecrets = append(envFromSecrets, project+"-shared", "kuso-instance-shared")
```

It appears TWICE. Replace the FIRST occurrence (the one preceded by the comment ending `...the pod boots cleanly even when no shared secret has been set.`) with:

```go
	envFromSecrets = append(envFromSecrets, kube.SharedSecretNames(project)...)
```

- [ ] **Step 2: `services_ops.go` — second site**

Replace the SECOND occurrence of the same line (preceded by a closing `}` and a blank line, before `env := &kube.KusoEnvironment{`) with:

```go
	envFromSecrets = append(envFromSecrets, kube.SharedSecretNames(project)...)
```

The `kube` package is already imported in `services_ops.go` (the file constructs `kube.KusoEnvironment`). No import change.

- [ ] **Step 3: `env_groups.go`**

In `server-go/internal/projects/env_groups.go`, find the line:

```go
	addonConnSecrets = append(addonConnSecrets, project+"-shared", "kuso-instance-shared")
```

Replace it with:

```go
	addonConnSecrets = append(addonConnSecrets, kube.SharedSecretNames(project)...)
```

The `kube` package is already imported in `env_groups.go` (it constructs `kube.KusoEnvironment`). No import change.

- [ ] **Step 4: Verify build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 5: Run the projects package tests**

Run (from `server-go/`): `go test ./internal/projects/`
Expected: PASS — this is a behavior-preserving change, existing tests must stay green.

- [ ] **Step 6: Commit**

```bash
git add server-go/internal/projects/services_ops.go server-go/internal/projects/env_groups.go
git commit -m "refactor(projects): build envFromSecrets via kube.SharedSecretNames"
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

- [ ] **Step 3: Confirm the three sites use the helper**

Run (from repo root):
```bash
grep -rn "SharedSecretNames" server-go/internal/addons/addons.go server-go/internal/projects/services_ops.go server-go/internal/projects/env_groups.go
```
Expected: `addons.go` → 1 occurrence, `services_ops.go` → 2, `env_groups.go` → 1.

- [ ] **Step 4: Confirm no inline shared-secret literal remains**

Run (from repo root):
```bash
grep -rn '"kuso-instance-shared"' server-go/internal/addons server-go/internal/projects
```
Expected: NO output (the only place the literal `"kuso-instance-shared"` should now live is inside `kube.SharedSecretNames` in `selectors.go`).

- [ ] **Step 5: Final commit (only if cleanup was needed)**

```bash
git add -A server-go
git commit -m "chore: envFromSecrets fix verification cleanup"
```
If nothing changed, skip.

---

## Rollout (post-merge — performed manually, not part of subagent execution)

After this plan is merged:
1. Cut a patch release (v0.13.11) via `KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh v0.13.11`.
2. `kuso upgrade --version v0.13.11` to roll the cluster.
3. distill's env CRs were already manually patched to include `distill-shared`; the fix ensures the next addon operation's `RefreshEnvSecrets` will no longer drop it. No further distill action needed.

---

## Self-Review Notes

- **Spec coverage:** Spec §Change 1 (helper in `kube`) → Task 1 (+ test Task 2). §Change 2 (fix `RefreshEnvSecrets`) → Task 3 (+ tests Task 4). §Change 3 (route the two correct paths) → Task 5. §Testing → Tasks 2, 4, 6. §Self-Healing → covered by Task 3's fix (the fan-out now writes the complete list, healing existing CRs on next addon op) and noted in the rollout section. §Rollout → the post-merge section.
- **Placeholder scan:** every code step shows exact code. Task 3 Step 2 notes "confirm the parameter name" — that is a deliberate read-first instruction (the function signature must be read to use the right identifier), not a vague placeholder; the surrounding code is concrete.
- **Type consistency:** `kube.SharedSecretNames(project string) []string` is defined in Task 1 and called identically in Tasks 3 and 5 — `append(slice, kube.SharedSecretNames(project)...)` everywhere. The test expectations in Task 4 (`["alpha-pg-conn","alpha-shared","kuso-instance-shared"]`) match the helper's output (`<project>-shared`, then `kuso-instance-shared`) appended after the conn-secrets.
