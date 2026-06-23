# Per-environment Addon Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `kuso environment add` provision a new named env with its own addons (postgres clone + own redis + own s3) by default, instead of sharing the project's production addons.

**Architecture:** Generalize the existing PR-preview clone path (`previewdb.EnsurePRAddons`, keyed on `pr-N`) into an env-scope-keyed core (`EnsureEnvAddons`, keyed on the env name). Wire that into `AddEnvironment` so a non-production env's `EnvFromSecrets` point at the per-env clones. `EnsurePRAddons` becomes a thin wrapper so preview behavior is unchanged.

**Tech Stack:** Go (server-go), Kubernetes client (controller-runtime dynamic + typed), cobra CLI (cli/cmd/kusoCli), resty API client (cli/pkg/kusoApi).

## Global Constraints

- Go module `kuso`; server logic in `server-go/internal`, CLI in `cli/`. Run tests with `go test ./...` from the relevant module dir (`server-go` or `cli`).
- Addon naming is canonical: CR `<project>-<short>` (`addons.CRName`), short = strip `<project>-` prefix (`addons.ShortName`), conn secret `<addonCR>-conn` (`addons.ConnSecretName`).
- Env scoping label is `kube.LabelEnv` (`kuso.sislelabs.com/env`). A named env's scope value is its env name (`staging`); a preview env's is `preview-pr-<N>`.
- `AddEnvironment` already reserves env names `production` and `pr-*` — so a named env's scope can never collide with a preview clone scope.
- Build the CLI after server-go API changes: `cd cli && go build -o /tmp/kuso ./cmd`.
- Preview behavior (PR envs) MUST remain identical — its tests are the regression gate.

---

### Task 1: Generalize the clone core from PR-keyed to env-scope-keyed (`previewdb`)

Replace the `prNumber int` parameter that threads through the seed/migrate chain with an `envScope string` (the full `LabelEnv` value), so the same machinery serves named envs and PR envs. This is a mechanical refactor with NO behavior change — `EnsurePRAddons` passes `"preview-pr-<N>"` as the scope.

**Files:**
- Modify: `server-go/internal/previewdb/previewdb.go` (the `seedAsync`, `seedAndMigrate` signatures + `EnsurePRAddons` clone loop)
- Modify: `server-go/internal/previewdb/migrate.go` (`migrateAfterSeed` label build)
- Test: `server-go/internal/previewdb/previewdb_test.go`, `migrate_test.go` (existing — must stay green)

**Interfaces:**
- Produces (used by Task 2): `EnsureEnvAddons(ctx context.Context, project, envScope string, opts EnvAddonOpts) ([]string, error)` and `type EnvAddonOpts struct { Kinds []string; SeedFromConn string }`.

- [ ] **Step 1: Replace `prNumber int` with `envScope string` in the seed chain (no behavior change).**

In `previewdb.go`, change the signatures:
```go
func (c *Cloner) seedAsync(ctx context.Context, ns, project, sourceFQN, cloneFQN string, instancePG bool, envScope string) {
```
```go
func (c *Cloner) seedAndMigrate(ctx context.Context, ns, project, sourceFQN, cloneFQN string, envScope string) error {
```
In `migrate.go`, change:
```go
func (c *Cloner) migrateAfterSeed(ctx context.Context, ns, project string, envScope string, cloneFQN string, nonce int64) {
	selector := kube.LabelSelector(map[string]string{
		kube.LabelEnv: envScope,
	})
	// ... replace the old fmt.Sprintf("preview-pr-%d", prNumber) and any
	//     "pr" log fields with envScope ...
```
Update every internal call site so the scope string is threaded through (`seedAndMigrate(... , envScope)`, `migrateAfterSeed(..., envScope, ...)`). Search for `prNumber` in both files and replace each use that built `"preview-pr-%d"` with the passed `envScope` directly.

- [ ] **Step 2: Add `EnsureEnvAddons` as the generalized core; make `EnsurePRAddons` a wrapper.**

In `previewdb.go`, add the options type and the core function (lift the body of the current `EnsurePRAddons`, parameterizing the scope + kinds + seed):
```go
// EnvAddonOpts controls how EnsureEnvAddons provisions a named env's addons.
type EnvAddonOpts struct {
	// Kinds limits which addon kinds get a per-env instance. Empty = postgres only
	// (the historical preview default). Values: "postgres", "redis", "s3".
	Kinds []string
	// SeedFromConn, when set, is the SOURCE postgres conn-secret name to pg_dump
	// from into a postgres clone. Empty = the clone starts empty.
	SeedFromConn string
}

// EnsureEnvAddons creates per-env instances of the project's stateful addons,
// scoped by the kuso.sislelabs.com/env label = envScope, and returns the clones'
// conn-secret names (callers swap these into the env's EnvFromSecrets). Idempotent.
// Postgres clones are seeded only when opts.SeedFromConn is set; redis/s3 instances
// are always fresh/empty.
func (c *Cloner) EnsureEnvAddons(ctx context.Context, project, envScope string, opts EnvAddonOpts) ([]string, error) {
	if c == nil || c.Addons == nil {
		return nil, nil
	}
	wantKind := func(k string) bool {
		if len(opts.Kinds) == 0 {
			return k == "postgres"
		}
		for _, x := range opts.Kinds {
			if x == k {
				return true
			}
		}
		return false
	}
	ns := c.namespaceFor(ctx, project)
	sources, err := c.Addons.List(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	var connSecrets []string
	for i := range sources {
		s := &sources[i]
		if !wantKind(s.Spec.Kind) {
			continue
		}
		// Skip addons that are themselves env-scoped (a clone) — never clone a clone.
		if s.Labels[kube.LabelEnv] != "" {
			continue
		}
		shortSrc := addons.ShortName(project, s.Name)
		if isPreviewCloneName(shortSrc) {
			continue
		}
		cloneShort := fmt.Sprintf("%s-%s", shortSrc, envScope)
		cloneFQN := addons.CRName(project, cloneShort)
		instancePG := s.Spec.UseInstanceAddon != ""

		if existing, _ := c.Kube.GetKusoAddon(ctx, ns, cloneFQN); existing == nil {
			if _, err := c.Addons.Add(ctx, project, addons.CreateAddonRequest{
				Name:             cloneShort,
				Kind:             s.Spec.Kind,
				Version:          s.Spec.Version,
				Size:             s.Spec.Size,
				HA:               false,
				StorageSize:      s.Spec.StorageSize,
				Database:         s.Spec.Database,
				UseInstanceAddon: s.Spec.UseInstanceAddon,
				ExtraLabels: map[string]string{
					kube.LabelEnv: envScope,
				},
			}); err != nil {
				c.Logger.Warn("env addon clone create", "addon", cloneShort, "scope", envScope, "err", err)
				return nil, fmt.Errorf("provision %s for env %s: %w", cloneShort, envScope, err)
			}
			c.Logger.Info("env addon provisioned", "source", shortSrc, "clone", cloneShort, "scope", envScope)
		}
		connSecrets = append(connSecrets, addons.ConnSecretName(cloneFQN))

		// Seed only postgres clones, and only when a source conn was given.
		if s.Spec.Kind == "postgres" && opts.SeedFromConn != "" {
			if !c.tryAcquireSeed(cloneFQN) {
				continue
			}
			seedCtx, cancel := context.WithTimeout(c.BaseCtx, 30*time.Minute)
			go func(src, clone string, isInstancePG bool) {
				defer cancel()
				defer c.releaseSeed(clone)
				c.seedAsync(seedCtx, ns, project, src, clone, isInstancePG, envScope)
			}(addons.CRName(project, s.Name), cloneFQN, instancePG)
		}
	}
	return connSecrets, nil
}
```
Then replace the body of `EnsurePRAddons` with a wrapper that preserves the historical preview behavior (postgres-only, seeded from the source, plus the preview-specific source-tracking labels):
```go
func (c *Cloner) EnsurePRAddons(ctx context.Context, project string, prNumber int) ([]string, error) {
	scope := fmt.Sprintf("preview-pr-%d", prNumber)
	// Preview envs seed from the project's source postgres addon. Resolve its
	// conn-secret so EnsureEnvAddons performs the same pg_dump|psql as before.
	sources, err := c.Addons.List(ctx, project)
	if err != nil {
		return nil, fmt.Errorf("list addons: %w", err)
	}
	seedConn := ""
	for i := range sources {
		s := &sources[i]
		if s.Spec.Kind == "postgres" && s.Labels[kube.LabelEnv] == "" && !isPreviewCloneName(addons.ShortName(project, s.Name)) {
			seedConn = addons.ConnSecretName(addons.CRName(project, s.Name))
			break
		}
	}
	return c.EnsureEnvAddons(ctx, project, scope, EnvAddonOpts{Kinds: []string{"postgres"}, SeedFromConn: seedConn})
}
```
> NOTE: if the existing preview-delete sweep relies on the `kuso.sislelabs.com/preview-pr` / `preview-source` labels (it does, in `DeleteEnvironment` and `DeletePRAddons`), KEEP stamping them for the preview path. Add them back in the `EnsurePRAddons` wrapper by passing an extra label set — extend `EnvAddonOpts` with `ExtraLabels map[string]string` and merge it into the `ExtraLabels` map in `EnsureEnvAddons` alongside `LabelEnv`. Set `ExtraLabels: {"kuso.sislelabs.com/preview-pr": N, "kuso.sislelabs.com/preview-source": shortSrc}` from the wrapper. (`preview-source` is per-addon, so compute it inside the loop — simplest is to keep the preview-specific labels applied inside `EnsureEnvAddons` only when a sentinel is present; pragmatically, add `PreviewPR string` to `EnvAddonOpts` and let the core stamp the two preview labels when it's non-empty.)

- [ ] **Step 3: Run the existing previewdb tests — they must still pass (regression gate).**

Run: `cd server-go && go test ./internal/previewdb/... -v`
Expected: PASS (all existing preview/migrate tests green — the refactor is behavior-preserving).

- [ ] **Step 4: Add a unit test for `EnsureEnvAddons` clone naming + labels + kind filtering.**

In `previewdb_test.go`, add a test using the package's existing fake/kube test harness (mirror the setup of the nearest existing `EnsurePRAddons` test):
```go
func TestEnsureEnvAddons_NamesAndLabelsAndKinds(t *testing.T) {
	// Arrange: project "alpha" with a postgres addon "alpha-pg" and a redis "alpha-cache".
	// (Reuse the test harness that the existing EnsurePRAddons test builds.)
	c, fake := newTestCloner(t, /* project */ "alpha",
		fakeAddon("alpha-pg", "postgres"),
		fakeAddon("alpha-cache", "redis"),
	)

	// Act: provision env "staging" with postgres + redis, empty (no seed).
	conns, err := c.EnsureEnvAddons(ctx, "alpha", "staging",
		EnvAddonOpts{Kinds: []string{"postgres", "redis"}})
	if err != nil {
		t.Fatalf("EnsureEnvAddons: %v", err)
	}

	// Assert: two clones created, named "<short>-staging", labeled env=staging.
	wantConns := []string{"alpha-pg-staging-conn", "alpha-cache-staging-conn"}
	if !sameSet(conns, wantConns) {
		t.Fatalf("conns = %v, want %v", conns, wantConns)
	}
	pg := fake.getAddon("alpha-pg-staging")
	if pg == nil || pg.Labels[kube.LabelEnv] != "staging" {
		t.Fatalf("clone alpha-pg-staging missing or mislabeled: %+v", pg)
	}
	cache := fake.getAddon("alpha-cache-staging")
	if cache == nil || cache.Labels[kube.LabelEnv] != "staging" {
		t.Fatalf("clone alpha-cache-staging missing or mislabeled: %+v", cache)
	}
}
```
> Adapt `newTestCloner`/`fakeAddon`/`fake.getAddon`/`sameSet` to whatever helpers the existing previewdb test file already provides; do not invent a new harness. If the existing tests construct the `Cloner` + a fake `addons.Service` inline, copy that construction.

- [ ] **Step 5: Run the new test, fix until green.**

Run: `cd server-go && go test ./internal/previewdb/ -run TestEnsureEnvAddons -v`
Expected: PASS.

- [ ] **Step 6: Commit.**

```bash
cd /Users/sisle/code/work/kuso
git add server-go/internal/previewdb/previewdb.go server-go/internal/previewdb/migrate.go server-go/internal/previewdb/previewdb_test.go
git commit -m "feat(previewdb): generalize PR clone path into env-scope-keyed EnsureEnvAddons"
```

---

### Task 2: Wire per-env addons into `AddEnvironment` + the request/CLI flags

Make `AddEnvironment` call `EnsureEnvAddons` for non-production named envs (unless `--share-addons`), swap the project addon conn-secrets in `EnvFromSecrets` for the clone conn-secrets, and add the CLI flags.

**Files:**
- Modify: `server-go/internal/projects/services_ops.go` (`CreateEnvRequest` struct + `AddEnvironment` body, around the `envFromSecrets` assembly ~line 770–810 and CR build ~line 813)
- Modify: `server-go/internal/projects/envs_ops.go` (`DeleteEnvironment` sweep — also delete `LabelEnv == env` addons for named envs)
- Modify: `cli/pkg/kusoApi/projects.go` (`CreateEnvRequest` mirror + `AddEnvironment`)
- Modify: `cli/cmd/kusoCli/environment.go` (new flags)
- Test: `server-go/internal/projects/env_addons_test.go` (new)

**Interfaces:**
- Consumes: `EnsureEnvAddons(ctx, project, envScope, EnvAddonOpts{Kinds, SeedFromConn, PreviewPR})` from Task 1.
- The `Service` already holds the previewdb cloner? Verify: check whether `projects.Service` has a field for the `previewdb.Cloner` (the dispatcher uses it via an interface `PreviewDB`). If `projects.Service` does NOT already reference it, add a minimal interface field `EnvAddons` with method `EnsureEnvAddons(...)` and inject it where the Service is constructed (mirror how `s.AddonConnSecrets` func field is injected).

- [ ] **Step 1: Add request fields (server + CLI mirror).**

In `services_ops.go`:
```go
type CreateEnvRequest struct {
	Name         string `json:"name"`
	Branch       string `json:"branch"`
	HostOverride string `json:"host,omitempty"`
	// ShareAddons opts the env OUT of per-env addon provisioning: it shares the
	// project's addons with production (the legacy behavior). Default false.
	ShareAddons bool `json:"shareAddons,omitempty"`
	// SeedFrom, when set, seeds the env's new postgres DB from the named source
	// env's database (pg_dump|psql). Empty = the env's DB starts empty.
	SeedFrom string `json:"seedFrom,omitempty"`
	// Addons overrides which stateful addon kinds get a per-env instance.
	// Empty = every stateful kind the project has (postgres, redis, s3).
	Addons []string `json:"addons,omitempty"`
}
```
Mirror the same three fields in `cli/pkg/kusoApi/projects.go`'s `CreateEnvRequest`.

- [ ] **Step 2: In `AddEnvironment`, provision per-env addons + swap conn-secrets.**

After the existing `envFromSecrets` is assembled and BEFORE the `env := &kube.KusoEnvironment{...}` literal (services_ops.go ~line 810), insert:
```go
	// Per-env addons: by default a named env gets its OWN addons (own DB, redis,
	// s3) so staging/qa never touch production data. --share-addons opts out.
	if !req.ShareAddons && s.EnvAddons != nil {
		// Resolve the seed source's postgres conn-secret if --seed-from was given.
		seedConn := ""
		if req.SeedFrom != "" {
			conn, ok := s.postgresConnForEnv(ctx, project, service, req.SeedFrom)
			if !ok {
				return nil, fmt.Errorf("%w: --seed-from %q: no postgres database found for that env", ErrInvalid, req.SeedFrom)
			}
			seedConn = conn
		}
		clones, err := s.EnvAddons.EnsureEnvAddons(ctx, project, req.Name, previewdb.EnvAddonOpts{
			Kinds:        req.Addons, // empty → EnsureEnvAddons defaults; see note below
			SeedFromConn: seedConn,
		})
		if err != nil {
			return nil, fmt.Errorf("provision env addons: %w", err)
		}
		// Drop the PROJECT addon conn-secrets from envFromSecrets (keep shared /
		// instance / per-service / foo-conn secrets), then append the clones.
		projectAddons := s.listProjectAddonConnSecrets(ctx, project)
		envFromSecrets = dropProjectAddonConns(envFromSecrets, projectAddons)
		envFromSecrets = append(envFromSecrets, clones...)
	}
```
> NOTE on the default kinds: the spec wants the DEFAULT (empty `req.Addons`) to mean "every stateful kind the project has (postgres+redis+s3)", but `EnsureEnvAddons` treats empty as postgres-only (preview parity). Resolve by having `AddEnvironment` compute the default kinds when `req.Addons` is empty: list the project's addons, collect the distinct stateful kinds among {postgres, redis, s3}, and pass that explicit slice to `EnsureEnvAddons`. Add a helper `s.statefulAddonKinds(ctx, project) []string`. This keeps `EnsureEnvAddons`'s own default (postgres-only) correct for the preview wrapper while giving named envs full isolation.

Add the helper `dropProjectAddonConns(secrets, projectAddonConns []string) []string` next to `filterEnvFromForSubscription` (subscribed_addons.go) — it removes any secret that IS a project addon conn-secret:
```go
// dropProjectAddonConns removes the project's own addon conn-secrets from a
// list, leaving shared/instance/per-service/foo-conn secrets intact. Used when an
// env swaps the shared addons for its own per-env clones.
func dropProjectAddonConns(secrets, projectAddonConns []string) []string {
	drop := make(map[string]bool, len(projectAddonConns))
	for _, n := range projectAddonConns {
		drop[n] = true
	}
	out := secrets[:0:0]
	for _, s := range secrets {
		if !drop[s] {
			out = append(out, s)
		}
	}
	return out
}
```
And add `s.postgresConnForEnv(ctx, project, service, envName) (string, bool)` and `s.statefulAddonKinds(ctx, project) []string` helpers in services_ops.go (list addons; for the seed-source, prefer the env-scoped clone `<short>-<envName>` if it exists, else the project addon for `production`).

- [ ] **Step 3: Generalize the delete sweep for named envs (envs_ops.go).**

In `DeleteEnvironment`, after the existing preview-pr sweep, add a sweep for the named env scope so deleting `staging` drops its `env=staging` addons:
```go
	// Named env (staging/qa/...): delete every addon scoped to this env via the
	// canonical env label, so the env's own DB/redis/s3 + their PVCs are removed.
	if pr := previewPRNumber(env, serviceFQN); pr == "" {
		sel := kube.LabelSelector(map[string]string{
			kube.LabelProject: project,
			kube.LabelEnv:     env,
		})
		if list, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{LabelSelector: sel}); lerr == nil {
			for i := range list.Items {
				name := list.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					_ = derr
				}
			}
		}
	}
```

- [ ] **Step 4: Add the CLI flags (environment.go).**

In `cli/cmd/kusoCli/environment.go`, add flag vars + bind them, and populate the request:
```go
var (
	envAddBranch      string
	envAddHost        string
	envAddShareAddons bool
	envAddSeedFrom    string
	envAddAddons      []string
)
// in init(): on the `add` command:
addCmd.Flags().BoolVar(&envAddShareAddons, "share-addons", false, "share the project's addons instead of giving this env its own (legacy behavior)")
addCmd.Flags().StringVar(&envAddSeedFrom, "seed-from", "", "seed this env's postgres DB from the named source env (default: empty DB)")
addCmd.Flags().StringSliceVar(&envAddAddons, "addons", nil, "stateful addon kinds to provision per-env (default: all the project has)")
// in RunE, where the request is built:
req := kusoApi.CreateEnvRequest{
	Name:        args[2],
	Branch:      envAddBranch,
	HostOverride: envAddHost,
	ShareAddons: envAddShareAddons,
	SeedFrom:    envAddSeedFrom,
	Addons:      envAddAddons,
}
```
Update the command's long/example help to mention the new default + flags.

- [ ] **Step 5: Write the wire-in test (server).**

Create `server-go/internal/projects/env_addons_test.go`:
```go
func TestAddEnvironment_SwapsToPerEnvAddons(t *testing.T) {
	// Arrange: project "alpha", service "web" (==project false), production env with
	//   EnvFromSecrets = ["alpha-pg-conn", "alpha-shared", "alpha-web-secrets"].
	// Inject a fake EnvAddons whose EnsureEnvAddons("alpha","staging",...) returns
	//   ["alpha-pg-staging-conn"].
	s := newTestService(t, /* with fake EnvAddons + listProjectAddonConnSecrets→["alpha-pg-conn"] */)
	env, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil { t.Fatal(err) }

	got := env.Spec.EnvFromSecrets
	// alpha-pg-conn dropped; alpha-pg-staging-conn appended; non-addon secrets kept.
	assertContains(t, got, "alpha-pg-staging-conn")
	assertNotContains(t, got, "alpha-pg-conn")
	assertContains(t, got, "alpha-shared")
	assertContains(t, got, "alpha-web-secrets")
}

func TestAddEnvironment_ShareAddonsKeepsProjectConns(t *testing.T) {
	s := newTestService(t, /* same */)
	env, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging", ShareAddons: true})
	if err != nil { t.Fatal(err) }
	assertContains(t, env.Spec.EnvFromSecrets, "alpha-pg-conn")     // shared kept
	assertNotContains(t, env.Spec.EnvFromSecrets, "alpha-pg-staging-conn")
}
```
> Adapt `newTestService` / `assertContains` to the existing projects test harness (see `env_domains_test.go` for how a `Service` is built with fakes). Inject the fake `EnvAddons` via the same field you added in Task 2 Step 0; stub `listProjectAddonConnSecrets`.

- [ ] **Step 6: Run the wire-in tests + the full projects + previewdb suites.**

Run: `cd server-go && go test ./internal/projects/... ./internal/previewdb/... -v`
Expected: PASS (new tests green; existing tests unaffected).

- [ ] **Step 7: Build the CLI + server, vet.**

Run:
```bash
cd /Users/sisle/code/work/kuso/server-go && go build ./... && go vet ./internal/projects/... ./internal/previewdb/...
cd /Users/sisle/code/work/kuso/cli && go build -o /tmp/kuso ./cmd
```
Expected: both build clean, vet clean.

- [ ] **Step 8: Commit.**

```bash
cd /Users/sisle/code/work/kuso
git add server-go/internal/projects/services_ops.go server-go/internal/projects/envs_ops.go server-go/internal/projects/subscribed_addons.go server-go/internal/projects/env_addons_test.go cli/pkg/kusoApi/projects.go cli/cmd/kusoCli/environment.go
git commit -m "feat(env): kuso environment add provisions per-env addons by default (--share-addons to opt out)"
```

---

### Task 3: Manual verification against the cluster + update docs

**Files:**
- Modify: `cli/cmd/kusoCli/environment.go` (help text — confirm done in Task 2)
- Modify: `docs/EDIT_SAFETY.md` or a short note in `docs/` describing per-env addons (one paragraph) + CHANGELOG entry per the repo's release flow.

- [ ] **Step 1: Deploy the rebuilt server to the test cluster** (per the repo's release/deploy flow — see how server-go is shipped; if the dev cluster runs a built image, build + push + redeploy the kuso server). Confirm the CLI talks to it: `/tmp/kuso version`.

- [ ] **Step 2: Create a staging env on a throwaway project (or scubatony) and verify isolation.**

```bash
/tmp/kuso environment add scubatony internal-system staging --branch staging
/tmp/kuso get addons scubatony -o json   # expect scubatony-db-staging (+ redis/s3) labeled env=staging
```
Expected: per-env addons exist, labeled `kuso.sislelabs.com/env=staging`; the staging env's `DATABASE_URL` resolves to `scubatony-db-staging-conn`, NOT `scubatony-db-conn`.

- [ ] **Step 3: Confirm tunnel access to the isolated staging DB.**

```bash
/tmp/kuso db connect scubatony db-staging
```
Expected: a localhost DSN for the staging DB (separate from production `db`).

- [ ] **Step 4: Confirm deletion cleans up.**

```bash
/tmp/kuso project env delete scubatony scubatony-internal-system-staging
/tmp/kuso get addons scubatony -o json   # the *-staging addons are gone
```

- [ ] **Step 5: Update docs + CHANGELOG; commit.**

Add a short doc note (what per-env addons are, the `--share-addons`/`--seed-from` flags, that production is untouched) and a CHANGELOG entry. Commit.

---

## Self-Review

**Spec coverage:**
- Empty-by-default + `--seed-from` → Task 2 Step 1 (`SeedFrom`) + Task 1 (`SeedFromConn` only seeds when set). ✓
- Full isolation (postgres+redis+s3) → Task 2 Step 2 default-kinds note + `statefulAddonKinds`. ✓
- On-by-default + `--share-addons` opt-out → Task 2 Steps 1–2, 4. ✓
- Generalize previewdb, preview unchanged → Task 1 (wrapper + regression gate Step 3). ✓
- Deletion sweep for named envs → Task 2 Step 3. ✓
- CLI flags + help → Task 2 Step 4. ✓
- Tests (unit/regression/wire-in/manual) → Tasks 1.4, 1.3, 2.5, 3. ✓

**Open implementation detail flagged for the engineer (not a placeholder — a real decision to make at the code):** preview-specific labels (`preview-pr`, `preview-source`) must keep being stamped for the preview path; Task 1 Step 2's NOTE specifies threading them via `EnvAddonOpts.PreviewPR` (or an `ExtraLabels`/source-label mechanism). Confirm by reading `DeleteEnvironment`/`DeletePRAddons` before finalizing Task 1, and keep those tests green as the gate.

**Type consistency:** `EnsureEnvAddons` / `EnvAddonOpts{Kinds, SeedFromConn[, PreviewPR]}` used identically in Task 1 (def) and Task 2 (call). `CreateEnvRequest{ShareAddons, SeedFrom, Addons}` identical server + CLI. ✓
