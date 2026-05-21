# kuso Addon-Add Conn-Secret Race Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the read-after-write race where `addons.Add` provisions an addon but its `<name>-conn` secret never reaches existing services, because `RefreshEnvSecrets` enumerates addons via an eventually-consistent label-list that misses the just-created addon.

**Architecture:** Split `RefreshEnvSecrets` into a public delegate (unchanged signature) and an unexported variadic core `refreshEnvSecrets(ctx, project, extraConnSecrets ...string)` that unions explicitly-passed conn-secret names into the base list, de-duplicated. `addons.Add` passes the just-created addon's conn-secret name explicitly, so it is wired into every service regardless of cache timing.

**Tech Stack:** Go (kuso server-go), Kubernetes client-go, the dynamic fake client for tests.

**Verification model:** `go build ./...` and `go test ./...` from `server-go/`.

**Scope:** One file (`server-go/internal/addons/addons.go`) + its test file. Ships as a kuso release + cluster upgrade (rollout section).

---

## Task 1: Baseline build + tests

**Files:** none (verification)

- [ ] **Step 1: Baseline**

Run (from `server-go/`): `go build ./... && go test ./internal/addons/...`
Expected: both pass. If either fails, STOP and report BLOCKED.

---

## Task 2: Failing test — explicit conn secret survives a stale addon list

**Files:**
- Modify: `server-go/internal/addons/addons_test.go`

CONTEXT: `addons_test.go` has a `fakeService(t, seeds...)` helper and
existing `RefreshEnvSecrets` tests (`TestAdd_RefreshesEnvSecrets`,
`TestRefreshEnvSecrets_KeepsSharedSecrets`). The new test calls the
new unexported `refreshEnvSecrets` core directly with an `extra`
conn-secret arg and asserts it lands in every env's `envFromSecrets`
even though NO addon was seeded (simulating a label-list that does not
yet see the just-created addon).

- [ ] **Step 1: Write the failing test**

Append to `server-go/internal/addons/addons_test.go`:
```go
// TestRefreshEnvSecrets_ExplicitExtraSurvivesStaleList reproduces the
// addon-add read-after-write race: the addon label-list does not yet
// return the just-created addon (here: no addon seeded at all), but
// the explicit extraConnSecrets argument must still land in every
// env's envFromSecrets.
func TestRefreshEnvSecrets_ExplicitExtraSurvivesStaleList(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
	)

	// No addon seeded — the label-list inside refreshEnvSecrets returns
	// nothing, exactly as the watch cache would right after a create.
	if err := s.refreshEnvSecrets(context.Background(), "alpha", "alpha-cache-conn"); err != nil {
		t.Fatalf("refreshEnvSecrets: %v", err)
	}

	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	got := envCR.Spec.EnvFromSecrets
	found := false
	for _, g := range got {
		if g == "alpha-cache-conn" {
			found = true
		}
	}
	if !found {
		t.Fatalf("envFromSecrets %+v missing explicitly-passed alpha-cache-conn", got)
	}
}

// TestRefreshEnvSecrets_ExtraNotDuplicated confirms an extra conn
// secret that the addon list ALSO returns appears exactly once.
func TestRefreshEnvSecrets_ExtraNotDuplicated(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
	)
	// Seed the addon so the list DOES return alpha-pg; then also pass
	// alpha-pg-conn explicitly. It must not be duplicated.
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.refreshEnvSecrets(context.Background(), "alpha", "alpha-pg-conn"); err != nil {
		t.Fatalf("refreshEnvSecrets: %v", err)
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	count := 0
	for _, g := range envCR.Spec.EnvFromSecrets {
		if g == "alpha-pg-conn" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("alpha-pg-conn appears %d times in %+v, want exactly 1", count, envCR.Spec.EnvFromSecrets)
	}
}
```

- [ ] **Step 2: Run — verify it fails to COMPILE**

Run (from `server-go/`): `go test ./internal/addons/... 2>&1 | head -20`
Expected: COMPILE FAILURE — `s.refreshEnvSecrets undefined` (the
unexported core does not exist yet). This confirms the test targets
the new function. (A compile failure is the expected "red" here.)

---

## Task 3: Split `RefreshEnvSecrets` into delegate + variadic core

**Files:**
- Modify: `server-go/internal/addons/addons.go`

CONTEXT: The current `RefreshEnvSecrets(ctx, project) error` body lists
addons, builds `baseSecrets` (conn secrets + `SharedSecretNames`), then
patches every env. It is split: a thin public delegate keeps the
signature; the body moves to `refreshEnvSecrets` with a variadic
`extraConnSecrets ...string` that is unioned into `baseSecrets`,
de-duplicated.

- [ ] **Step 1: Replace `RefreshEnvSecrets` with the delegate + core**

In `server-go/internal/addons/addons.go`, the function currently begins
`func (s *Service) RefreshEnvSecrets(ctx context.Context, project string) error {`.
Replace the ENTIRE function with these two functions:
```go
// RefreshEnvSecrets recomputes the project's addon-conn secret list and
// rewrites every env's envFromSecrets. Public entrypoint with a stable
// signature — callers that have no just-created addon to account for
// (e.g. the delete path) use this directly.
func (s *Service) RefreshEnvSecrets(ctx context.Context, project string) error {
	return s.refreshEnvSecrets(ctx, project)
}

// refreshEnvSecrets is the core of RefreshEnvSecrets. extraConnSecrets
// names conn secrets that MUST be included even if the addon
// label-list does not return them yet.
//
// Why extraConnSecrets exists: addons.Add creates the KusoAddon CR and
// then refreshes env secrets immediately. The addon List() here is a
// label-selector query served from the eventually-consistent watch
// cache, so the just-created addon is frequently not yet visible —
// without an explicit hand-off its conn secret would be silently
// omitted from every service's envFromSecrets. The Add path passes the
// new addon's conn-secret name here to close that read-after-write
// race deterministically.
func (s *Service) refreshEnvSecrets(ctx context.Context, project string, extraConnSecrets ...string) error {
	addons, err := s.List(ctx, project)
	if err != nil {
		return err
	}
	// Build baseSecrets, de-duplicated: addon conn secrets, then the
	// project/instance-shared secrets, then any explicitly-passed
	// extras. seen guards against listing a conn secret twice when the
	// label-list DID return an addon that is also in extraConnSecrets.
	seen := make(map[string]bool)
	baseSecrets := make([]string, 0, len(addons)+len(extraConnSecrets)+2)
	addSecret := func(name string) {
		if name == "" || seen[name] {
			return
		}
		seen[name] = true
		baseSecrets = append(baseSecrets, name)
	}
	for _, a := range addons {
		addSecret(connSecretName(a.Name))
	}
	// Always carry the project-shared + instance-shared secrets. The
	// merge-patch below REPLACES spec.envFromSecrets wholesale, so any
	// entry not in this slice is dropped — omitting the shared secrets
	// here is the bug that silently stripped auth tokens, Stripe keys
	// and Discord bot tokens from every service's pods after an addon
	// add/remove.
	for _, name := range kube.SharedSecretNames(project) {
		addSecret(name)
	}
	// Explicitly-passed conn secrets — the read-after-write hand-off.
	for _, name := range extraConnSecrets {
		addSecret(name)
	}
	ns := s.nsFor(ctx, project)
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{
		kube.LabelProject: project,
	})
	if err != nil {
		return fmt.Errorf("list envs: %w", err)
	}
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
	return nil
}
```
NOTE: this preserves the existing body's logic exactly (addon conn
secrets + shared secrets + per-env service/env-scoped secrets, the
clone, the merge-patch) — the only additions are the `seen`/`addSecret`
de-dup and the `extraConnSecrets` union. Confirm `slices` is already
imported in the file (the original body used `slices.Clone`).

- [ ] **Step 2: Build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds. The `Add` call at the old `RefreshEnvSecrets(ctx, project)`
site still compiles (the public signature is unchanged) — Task 4
updates it.

- [ ] **Step 3: Run the addons tests**

Run (from `server-go/`): `go test ./internal/addons/...`
Expected: PASS — including the two new tests from Task 2
(`refreshEnvSecrets` now exists). The pre-existing tests
(`TestAdd_RefreshesEnvSecrets`, `TestRefreshEnvSecrets_KeepsSharedSecrets`,
`TestRefreshEnvSecrets_PerServiceIsolation`, `TestDelete_*`) must also
still pass — the public `RefreshEnvSecrets` behaves identically.

- [ ] **Step 4: Commit**

```bash
git add server-go/internal/addons/addons.go server-go/internal/addons/addons_test.go
git commit -m "fix(addons): refreshEnvSecrets accepts explicit conn secrets"
```

---

## Task 4: `addons.Add` passes the new addon's conn secret explicitly

**Files:**
- Modify: `server-go/internal/addons/addons.go`

CONTEXT: In `addons.Add`, after `created, err := createAddon(ctx, s, ns, addon)`
the code calls `s.RefreshEnvSecrets(ctx, project)`. `created` is the
KusoAddon CR — its `.Name` is the addon CR's full name; `connSecretName(created.Name)`
is its conn-secret name.

- [ ] **Step 1: Change the `Add` refresh call**

In `addons.Add`, find:
```go
	if err := s.RefreshEnvSecrets(ctx, project); err != nil {
		// Best-effort — the addon CR is in place; logs/admin can retry
		// the env refresh manually if this fails.
		return created, fmt.Errorf("addon created but env refresh failed: %w", err)
	}
```
Change the call to pass the new addon's conn secret explicitly:
```go
	// Pass the just-created addon's conn secret explicitly: the addon
	// List() inside refreshEnvSecrets is served from the watch cache
	// and frequently does not see this brand-new addon yet. The
	// explicit hand-off guarantees its conn secret is wired into every
	// existing service regardless of cache lag.
	if err := s.refreshEnvSecrets(ctx, project, connSecretName(created.Name)); err != nil {
		// Best-effort — the addon CR is in place; logs/admin can retry
		// the env refresh manually if this fails.
		return created, fmt.Errorf("addon created but env refresh failed: %w", err)
	}
```

- [ ] **Step 2: Build**

Run (from `server-go/`): `go build ./...`
Expected: succeeds.

- [ ] **Step 3: Strengthen `TestAdd_RefreshesEnvSecrets` for the race**

The existing `TestAdd_RefreshesEnvSecrets` passes today because the
fake client's list is synchronous. To prove the Add path now survives a
stale list, add one focused assertion test. Append to
`addons_test.go`:
```go
// TestAdd_WiresConnSecretViaExplicitHandoff confirms Add wires the new
// addon's conn secret into existing services even though Add relies on
// the explicit hand-off rather than the (cache-lagged) addon list.
func TestAdd_WiresConnSecretViaExplicitHandoff(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProj("alpha"),
		seedEnv("alpha", "web", "production", "alpha-web-production"),
		seedEnv("alpha", "api", "production", "alpha-api-production"),
	)
	if _, err := s.Add(context.Background(), "alpha", CreateAddonRequest{Name: "cache", Kind: "redis"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	for _, envName := range []string{"alpha-web-production", "alpha-api-production"} {
		envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", envName)
		if err != nil {
			t.Fatalf("get env %s: %v", envName, err)
		}
		found := false
		for _, g := range envCR.Spec.EnvFromSecrets {
			if g == "alpha-cache-conn" {
				found = true
			}
		}
		if !found {
			t.Errorf("env %s envFromSecrets %+v missing alpha-cache-conn", envName, envCR.Spec.EnvFromSecrets)
		}
	}
}
```

- [ ] **Step 4: Run the addons tests**

Run (from `server-go/`): `go test ./internal/addons/...`
Expected: PASS — all tests including the new one.

- [ ] **Step 5: Commit**

```bash
git add server-go/internal/addons/addons.go server-go/internal/addons/addons_test.go
git commit -m "fix(addons): addon-add wires conn secret into existing services (closes watch-cache race)"
```

---

## Task 5: Full verification

**Files:** none (verification only)

- [ ] **Step 1: Full build + test**

Run (from `server-go/`): `go build ./... && go test ./...`
Expected: both pass. If an unrelated package has a pre-existing
failure (e.g. needs a live DB), note it; the `internal/addons` package
must be fully green.

- [ ] **Step 2: Confirm the wiring**

Run (from the kuso repo root):
```bash
grep -n "func (s \*Service) RefreshEnvSecrets\|func (s \*Service) refreshEnvSecrets" server-go/internal/addons/addons.go
grep -n "refreshEnvSecrets(ctx, project, connSecretName" server-go/internal/addons/addons.go
grep -n "RefreshEnvSecrets(ctx, project)" server-go/internal/addons/addons.go
```
Expected: both the public delegate and the unexported core exist; the
`Add` path calls `refreshEnvSecrets(...)` with `connSecretName(...)`;
the remaining bare `RefreshEnvSecrets(ctx, project)` call is the delete
path (unchanged, correct).

- [ ] **Step 3: Final commit (only if cleanup was needed)**

```bash
git add -A server-go
git commit -m "chore: addon-add race fix verification cleanup"
```
If nothing changed, skip.

---

## Rollout (post-merge — performed manually, not part of subagent execution)

1. Cut a kuso release: from the kuso repo root,
   `KUSO_RELEASE_COMMIT=1 KUSO_RELEASE_GH=1 KUSO_RELEASE_CLI=1 ./hack/release.sh v0.13.14`
   (current latest tag is v0.13.13). If the release script fails on a
   dirty working tree because `CHANGELOG.archive.md` was rewritten by
   git-cliff, commit that as `chore: archive older changelog entries`
   and re-run — this is the known release-script behavior.
2. Upgrade the cluster: `kuso upgrade --version v0.13.14`.
3. Verify: provision a throwaway addon into a project that already has
   services (`kuso project addon add <proj> tmpcache --kind redis`),
   confirm the existing services' env CRs gain `<proj>-tmpcache-conn`
   automatically (no manual patch), then remove the throwaway addon.

distill's already-provisioned `distill-cache` addon was manually wired
this session — the fix prevents the next occurrence; no retroactive
action is needed for distill.

---

## Self-Review Notes

- **Spec coverage:** the `RefreshEnvSecrets` delegate + `refreshEnvSecrets`
  variadic core with de-dup → Task 3. `addons.Add` passing the explicit
  conn secret → Task 4. The race-reproduction test (explicit extra
  survives a stale list) + the no-duplicate test → Task 2; the Add-path
  wiring test → Task 4. Delete path left unchanged → verified in Task 5
  Step 2. Rollout (release + cluster upgrade) → Rollout section.
- **Placeholder scan:** every code step shows complete code. Task 2
  Step 2 deliberately expects a COMPILE failure (the test references
  the not-yet-existing `refreshEnvSecrets`) — that is the TDD "red",
  concrete and described, not a placeholder.
- **Type consistency:** `refreshEnvSecrets(ctx context.Context, project
  string, extraConnSecrets ...string) error` (Task 3) is called as
  `s.refreshEnvSecrets(ctx, "alpha", "alpha-cache-conn")` in the tests
  (Task 2) and `s.refreshEnvSecrets(ctx, project, connSecretName(created.Name))`
  in `Add` (Task 4) — all match the variadic signature. `RefreshEnvSecrets(ctx,
  project)` keeps its exact original signature, delegating. `connSecretName`
  and `buildEnvFromSecretsPatch` are existing unexported helpers in the
  same file.
