# Architecture Review — Pass 3 (2026-05-12)

Scope: `main` @ v0.9.79, post-cleanup state per CLAUDE.md. Skipped findings already in REVIEW_2026-05-12-followup.md (apiv1 partial shipped, propagators swept, facades deleted, schema-drift detection landed). New review focused on findings that have hardened since the last pass, or that the cleanup work uncovered.

---

## A. Half-finished abstractions

### A-01 · P0 · apiv1 still only carries Project — every other DTO is hand-rolled twice (or three times)
The shared module declares "single source of truth for wire shape" (`api/apiv1/doc.go:7-9`) and currently delivers exactly one file: `projects.go`. The server-side handler imports apiv1 for `CreateProjectRequest`/`UpdateProjectRequest` (`handlers/projects.go:14, 233, 266`), but `AddService`, `PatchService`, `AddDomain`, `SetEnvVar`, `AddEnvironment`, `CreateEnvGroup`, `Add` (addon), and `Update` (addon) all decode into local `projects.*` / `addons.*` types (`handlers/projects.go:327, 420, 442, 482, 773, 866`; `handlers/addons.go:159, 196`). The CLI re-hand-rolls `CreateServiceRequest`, `PatchServiceRequest`, `SetEnvVarRequest`, `CreateAddonRequest`, `UpdateAddonRequest`, `AddDomainRequest`, `PatchServiceDomain`, `ServiceImageSpec`, `AddonExternalRequest`, `UpdateAddonBackup` in `cli/pkg/kusoApi/projects.go:48-92, 158-279`, each with comments explicitly noting "mirrors the server's X — duplicated here so the CLI doesn't have to import server-go/". The MCP server ships a *third* set in `mcp/internal/types/v2.go:6-107`. The promised contract is 5% delivered. The class of bug it was created to kill (silent CLI/server field drift) is alive in nine more shapes.
**Cost-of-fix:** 1 day to move every body-bearing DTO into `api/apiv1/{services,addons,envs,builds,secrets}.go`, then make handlers + CLI + MCP import-and-alias. **Recommendation:** finish the migration in one PR or revert apiv1 to a comment and stop pretending. Halfway is worst-of-both.

### A-02 · P0 · Web TS types diverge from the server enum — `runtime` accepts "worker" and "image", TS only declares 4 values
`server-go/internal/projects/services_ops.go:62` validates `runtime ∈ {dockerfile, nixpacks, buildpacks, static, worker, image}` (six values), but `web/src/types/projects.ts:45` declares `runtime?: "dockerfile" | "nixpacks" | "buildpacks" | "static"` (four). The TS type narrows out two real values the server accepts. Any UI that uses this for an exhaustive switch or `as const` enum will mis-render or refuse to compile when a worker / image service appears. Symptom of the broader "no shared schema" problem: web types are written by hand against the kube CR shape; nothing checks them.
**Cost-of-fix:** 4h to wire `tygo` (already discussed in `docs/REVIEW_2026-05-12.md` A-P1-3) or 5min to add `| "worker" | "image"` and accept the manual contract. **Recommendation:** add tygo against `api/apiv1` once A-01 lands; until then, hand-fix the runtime union and add a `// MIRROR: kube/types.go` warning header.

### A-03 · P1 · `invalidateDescribe` is gone in code, alive in three comments
The cache + helper were removed (`projects/projects.go:62-66` is the gravestone) but comments at `projects/projects.go:62`, `projects.go:64`, and `projects_ops.go:53` still narrate the contract as if callers must invoke it. A reader new to the package will look for the function, find no callers, and waste time reconstructing what was supposed to be there. Same pattern in `services_ops.go:1502-1534` where comments reference now-deleted per-field propagators (`propagateDomainsToEnvs`, `propagateInternalToEnvs`).
**Cost-of-fix:** 15 min `grep -r` + delete. **Recommendation:** sweep all `propagate{Domains,Internal,Volumes,Placement}ToEnvs` and `invalidateDescribe` comment references; replace with "see `propagateChangedToEnvs`".

### A-04 · P1 · `coolify/` Coolify-side import logic is duplicated in `cli/cmd/kusoCli/migrate.go` and `handlers/import_coolify.go` with subtle behaviour drift
The shared `coolify/` module covers the *Coolify client* (good — was the v0.9.79 cleanup target) but the import-side mapping logic was never promoted. `slugifyName`, `runtimeForBuildPack`, `parseFirstPort`, `assignKusoSlugs`/`assignCoolifySlugs`, `coolifyItemUUID`/`coolifyItemKind`, `coolifyServiceSlug`/`serviceSlugFromApp` exist twice (cli `migrate.go:410-525`, server `import_coolify.go:412-525`). They have *already drifted*:
  - `slugifyName` clamps to 50 chars in CLI (`migrate.go:504`), 63 chars in server (`import_coolify.go:521`). Same input, different slug → preview verdict can disagree with CLI apply.
  - `runtimeForBuildPack` returns `""` for unknown in CLI (`migrate.go:467`), `"dockerfile"` for unknown in server (`import_coolify.go:488`).
  - `parseFirstPort` accepts `"3000:3000"` in server, only bare integers in CLI.

This is the exact class of bug that motivated the `coolify/` promotion in the first place — only the bottom half got promoted.
**Cost-of-fix:** 2h to move into `coolify/mapping.go` (or `coolify/kusoshape.go`), pick one canonical behaviour per helper, delete the dupes. **Recommendation:** promote, with explicit choice on the 50/63 char clamp (server is right; CLI was wrong) and write a regression test against the chosen contract.

### A-05 · P1 · Three near-identical secret managers (`secrets`, `projectsecrets`, `instancesecrets`) share zero code
`projectsecrets.Service`, `instancesecrets.Service`, and `secrets.Service` all do "manage a kube Secret with `ListKeys`/`SetKey`/`UnsetKey`" (`projectsecrets/projectsecrets.go:58-143`, `instancesecrets/instancesecrets.go:47-97`, `secrets/`). The diffs are in scoping (per-service vs per-project vs per-instance) and constants. The common 80% — read-modify-write a Secret CR with metadata patch — is rewritten three times.
**Cost-of-fix:** 4h to extract `internal/kubesecret/store.go` with `Store{namespace, name}` + the three packages compose it. **Recommendation:** worth doing before v1.0; the duplication makes future "secret history" / "secret rotation" features 3x the work.

---

## B. Coupling smells

### B-01 · P0 · `handlers/import_coolify.go` houses 270 lines of provisioning orchestration in `applyCommit` — that's domain logic in a handler
`applyCommit` (`import_coolify.go:254-409`) groups items, derives slugs, creates projects, services, addons, env vars, and aggregates skip/error rows — all from the handler file. The handler should be `body → svc.Import → JSON`. There's no `internal/migration/` or `coolify.Importer` to take this. Same anti-pattern the previous review's A-P0-2 flagged for nodes; coolify-commit shipped fresh with the same shape. The seven helper funcs (`coolifyItemUUID`, `coolifyItemKind`, `assignCoolifySlugs`, `coolifyServiceSlug`, `runtimeForBuildPack`, `parseFirstPort`, `slugifyName`) sit in the handler too.
**Cost-of-fix:** 1 day — new `internal/migration/` (or `internal/coolifyimport/`) service with `Import(ctx, inv, picked) ImportResult`. Handler becomes 20 lines. **Recommendation:** do this *before* a second importer (Heroku, Render) gets added, otherwise the pattern locks in.

### B-02 · P1 · `Deps{}` god-bundle has 23 fields and 6 `if d.X != nil` nil-guards per handler block
`router.go:53-89` wires 23 dependencies; `mountAuthenticatedRoutes` (`router.go:328-468`) is 140 lines of `if d.X != nil { … Mount(r) }`. Every handler already has `Mount(chi.Router)` (30/30 do — confirmed by grep). The Module-interface refactor noted in `REVIEW_2026-05-12.md` A-P1-2 is trivially feasible.
**Cost-of-fix:** 4h. Define `type Module interface { Mount(r chi.Router) }`, push the nil-guard into each handler's New constructor (return nil when deps missing → skip in registry), iterate `[]Module`. **Recommendation:** the refactor pays for itself the next time a handler is added — currently that's a 3-place edit (Deps field + main.go wire + mountAuth block).

### B-03 · P1 · Access-control checks repeat in 152 places across 29 handler files
`requireProjectAccess` / `requireAdmin` / `requirePerm` are called 152 times across the handler tree (`grep -c requireProjectAccess|requireAdmin|requirePerm internal/http/handlers/`). Every route re-asserts its own access policy. A new route forgotten in this pattern is silently open. The right shape is per-route middleware groups; chi already supports `r.Group(func(r chi.Router) { r.Use(requireRole(RoleDeployer)) ... })`.
**Cost-of-fix:** 1 day. Group routes by required role in each handler's `Mount`. **Recommendation:** also build a `gosec`-style linter pass that fails if a `/api/projects/{project}/...` route doesn't have a project-access guard. Without it, the next added route is a tenancy bypass waiting to happen.

### B-04 · P1 · `projects.Service` is 56 methods spread across 9 files — the "split into projects/services/environments" task noted in original A-P1-1 was deferred and never picked up
`grep -c '^func (s \*Service)' internal/projects/*.go` returns 56. `services_ops.go` alone is 1647 lines; `env_groups.go` is 962 lines. The struct backs project, service, environment, env-vars, env-groups, drift, pods, and wake operations — every "X-on-a-service" feature lands here. The svcMutex GC, the metricsSF singleflight, and the namespace cache all share one struct. A reader looking for "where does setting service domains live?" has to grep across nine files.
**Cost-of-fix:** 1-2 days. Split into `projects.Service`, `services.Service`, `environments.Service`, `envgroups.Service`; each owns its mutex and cache shard. Handlers take three pointers instead of one. **Recommendation:** worth doing before v1 — the file count is a leading indicator that the next contributor will pile more onto `services_ops.go` until it's unreadable.

### B-05 · P2 · `GithubHandler` carries its own token-bucket implementation and per-installation map
`handlers/github.go:57-106` defines `ghTokenBucket`, `take`, `allowInstallation`, plus a `RunInstallLimiterGC`. That's domain logic (rate-limiting policy) inside the HTTP handler package. Same shape as the chi-router `inFlightLimit` semaphore — both are policy buried in plumbing.
**Cost-of-fix:** 2h. Move to `internal/ratelimit/installation.go`. **Recommendation:** low priority; flag for the next time someone touches webhook intake.

---

## C. Modular boundaries (`go.work` split)

### C-01 · P1 · `mcp/` module pulls its weight (separates MCP-SDK deps) but its `kusoclient` duplicates `cli/pkg/kusoApi` and its `types/v2.go` is a third DTO copy
`mcp/internal/kusoclient/client.go:54-60` reimplements `GetJSON`/`PostJSON` that `cli/pkg/kusoApi` already has. `mcp/internal/types/v2.go:6-107` is a fourth wire-shape copy alongside server-go domain types, apiv1, CLI hand-rolled types, and web TS. The `go.work` split made sense to keep MCP-SDK deps out of server-go, but the *types* belong in apiv1.
**Cost-of-fix:** 1-2h. MCP imports `api/apiv1` for shapes; keeps its own kusoclient (different transport story than CLI's resty). **Recommendation:** complete A-01 first, then MCP just imports.

### C-02 · P1 · `coolify/` module promoted to top-level (good), but contains *only* read-side client code — write-side mapping (handler + CLI both) stayed put
See A-04. The module is the shared *read* contract; the shared *write* contract isn't there. The split carries weight but leaves half its job undone.
**Cost-of-fix:** see A-04. **Recommendation:** promote `mapping.go` / `kusoshape.go` to `coolify/` and the module finally delivers what its name advertises.

### C-03 · P2 · `api/apiv1` module exists at the right boundary (no external deps, importable from any toolchain) but `apiv1.BoolPtr/IntPtr/StringPtr` aren't even apiv1-specific
The pointer helpers in `apiv1/projects.go:65-67` aren't wire-shape — they're generic Go ergonomics. They'd belong in `internal/ptr/` or `internal/genericptr/`. Re-exporting them through apiv1 is harmless but pollutes the contract module.
**Cost-of-fix:** 30 min. **Recommendation:** move to `internal/ptr/` once apiv1 fills out — leaving it for now is fine, low blast radius.

### C-04 · P2 · `cli/pkg/kusoApi/main.go` shares one `*resty.Request` across all method calls — a hidden state landmine, currently only safe because the CLI is single-threaded
`KusoClient.client` is one `*resty.Request` (`main.go:30-34`); every method does `k.client.SetBody(...)` then `k.client.Post(...)`. SetBody mutates shared state. Any future "run two CLI ops in goroutines" usage (parallel build trigger? batch revision fetch?) silently corrupts requests. The CLI gets away with it because of single-shot semantics — the moment someone writes a parallel test or a background-poll feature, it bites.
**Cost-of-fix:** 4h. Switch to `*resty.Client` and have each method call `.R().SetBody(...).Post(...)`. **Recommendation:** before any concurrent caller appears. Cheap insurance.

---

## D. CRD lifecycle paths (helm-operator-rendered KusoBuild Jobs vs the rest)

### D-01 · P0 · The helm-operator-renders-Jobs-for-KusoBuild seam is leaking — server-go's Cancel path has to delete 3 different things to keep the operator from resurrecting a build
`builds.Service.Cancel` (`builds/builds.go:733-810`) does:
1. Patch the CR with `spec.done=true`, blank `spec.image.tag` (to make the chart's guard short-circuit),
2. Delete the Job directly via `Clientset.BatchV1().Jobs(ns).Delete`,
3. Delete the helm release Secret with `owner=helm,name=<build>` selector (otherwise operator reconcile re-creates the Job).

The comment at `builds.go:752-764` documents a real outage (2026-05-05) where a cancelled build kept respawning every 30s until the operator was scaled to 0. The kusobuild chart's top-level no-op gate (`operator/helm-charts/kusobuild/templates/job.yaml:1-14`) is another belt-and-braces patch for the same seam.

This isn't "the seam still makes sense" — it's "the seam is paper-thin and we keep papering it." Every CR write that flows through the operator pays the helm-render tax (3 min reconcile, race against the watch); for a build that's a fast-changing transient resource, that's the wrong path. The follow-up review's A-P1-7 ("KusoBuild → Go controller, more urgent") still stands and has only gotten more urgent — Coolify import commit ships 50-500 KusoBuilds in a burst, exactly the path where the operator's per-CR render cost compounds.
**Cost-of-fix:** 3 days. ~200 lines of controller-runtime + a manager.Reconciler that owns Job lifecycle directly; CRD stays, chart becomes a no-op gate that the controller doesn't use. **Recommendation:** do this before v1.0. The current shape costs Cancel correctness, Coolify import throughput, and one more outage class.

### D-02 · P1 · Build phase/timing/message lives on CR annotations because helm-operator owns `.status`, but readers parse annotations into a typed BuildPhase elsewhere — there's no shared schema for those annotations
`builds/builds.go` writes `kuso.sislelabs.com/phase`, `kuso.sislelabs.com/completed-at`, `kuso.sislelabs.com/message` etc. via raw patch JSON. Readers (web canvas, drift checker, status rollup) re-derive the typed view from `metadata.annotations[…]`. If D-01 lands, this evaporates. Until then, the annotation contract is an undocumented schema.
**Cost-of-fix:** wraps into D-01. **Recommendation:** if D-01 is deferred past v1.0, at least extract `internal/buildmeta/annotations.go` with named constants + a typed read.

### D-03 · P1 · Helm-operator watches.yaml + the 5 charts under `operator/helm-charts/` carry validation that server-go also has — neither side is canonical
`operator/helm-charts/kusoservice/templates/marker.yaml` and friends do partial validation (the chart fails to render if `spec.repo.url` is empty for runtime=dockerfile, for example), while `projects.Service.AddService` does the canonical validation server-side. So a kuso.yml apply that misses a required field gets rejected by the server with a clean error; a CR poked in via raw kubectl gets a partial helm-render failure with a cryptic helm error. Not a bug, but a contract gap: the helm-chart-as-second-validator is doing real work nobody's tracking.
**Cost-of-fix:** doc, not code. **Recommendation:** add a `docs/CRD_VALIDATION_OWNERSHIP.md` matrix declaring which validator owns which field — chart or server. Without it, the schema-drift detection (just shipped) handles structure but not semantics.

---

## E. Web/server contract

### E-01 · P0 · See A-01 — handlers decode 90% of bodies into local domain types, apiv1 is decoration for one endpoint
Restating: `apiv1` is currently a fence around the project DTOs only. The wire contract for services, addons, env-vars, env-groups, builds, secrets, crons is whatever the server happens to JSON-tag on `projects.X` / `addons.X`. Renaming a tag without grep is the silent-drift bug apiv1 was created to prevent.

### E-02 · P1 · Web has zero generated types — every TS interface in `web/src/types/projects.ts` is a hand-translated mirror of `kube/types.go`
The `tygo` codegen suggestion in REVIEW_2026-05-12.md A-P1-3 hasn't shipped. Result: A-02 (runtime enum drift) plus the natural drift of every new field being typed twice. The web wizard for Coolify import (`web/src/app/(app)/settings/import/page.tsx:34-58`) has a *third* hand-roll of the `coolify.Item` shape.
**Cost-of-fix:** half a day to wire tygo into `make web`, gated on apiv1 actually carrying the types it claims to. **Recommendation:** do A-01 first, then tygo is straightforward.

### E-03 · P2 · `writeJSON` swallows encode errors silently (`handlers/projects.go:952-956`) — same pattern in every handler
Headers are written before the encode call, so there's no way to recover, but the silent `_ = json.NewEncoder(w).Encode(v)` means a panic in a `MarshalJSON` of any field becomes "client sees half a JSON body" with no server-side log. Cheap fix.
**Cost-of-fix:** 30 min. **Recommendation:** log encode errors at `Warn` level. Doesn't change observable behaviour, surfaces hidden bugs.

---

## F. Other findings

### F-01 · P2 · `audit.Service` is wired through every handler that mutates state but the nil-guard `if h.Audit != nil` repeats 30+ times — should be a no-op `Service` zero value
`handlers/projects.go:294-307, 538-549, 674-692, 716-737` all gate audit writes on `h.Audit != nil`. The audit service is supposed to be optional; the right shape is a no-op `Service{}` value that satisfies the same interface so callers never check. Same pattern needed for `Notifier`, `RecordRevision`.
**Cost-of-fix:** 1h. **Recommendation:** define `audit.NopService` and assign it in main when audit is disabled. Strip 30 nil-checks.

### F-02 · P2 · `KusoCron` is three shapes in one CRD — restating A-P0-4 from original review, not addressed
`internal/kube/types.go:549-602` still has Kind ∈ {"service", "http", "command"} with disjoint required-fields. The original review proposed splitting into `KusoServiceCron` + `KusoProjectCron`. Defer indefinitely is fine — no concrete bug pressure — but if you add a fourth flavor (kafka job? S3 sync?), do the split first.
**Cost-of-fix:** 1 day if/when. **Recommendation:** leave for now; revisit when the next cron flavor appears.

---

## Summary Table

| ID    | P  | Area                              | Symptom                                                                          | One-line fix                                              |
|-------|----|-----------------------------------|----------------------------------------------------------------------------------|-----------------------------------------------------------|
| A-01  | P0 | `api/apiv1`                       | apiv1 covers Project only; CLI + MCP hand-roll the rest; drift class still alive | Move all body-bearing DTOs into apiv1 in one PR           |
| A-02  | P0 | `web/src/types/projects.ts`       | `runtime` TS union missing `worker` and `image` — server accepts both            | Add to union (now); wire tygo against apiv1 (later)       |
| A-03  | P1 | `projects/projects.go`            | Stale comments narrate `invalidateDescribe` + dead propagators                   | grep-and-delete the comment references                    |
| A-04  | P1 | `coolify/` vs handler vs CLI      | slugify/runtime mapping duplicated, already drifted (50 vs 63 char clamp)        | Promote `coolify/mapping.go`; pick one canonical helper   |
| A-05  | P1 | `secrets`/`projectsecrets`/`instancesecrets` | 3 packages, same Secret-CRUD logic written three times                | Extract `internal/kubesecret/store.go`                    |
| B-01  | P0 | `handlers/import_coolify.go`      | 270-line `applyCommit` is domain logic in a handler                              | New `internal/migration/` service; handler shrinks to 20L |
| B-02  | P1 | `internal/http/router.go`         | `Deps{}` god-bundle (23 fields); 140-line nil-guard wiring                       | Define `Module interface { Mount(r) }`; iterate           |
| B-03  | P1 | `handlers/*`                      | 152 access-control re-checks across 29 files; one forgotten = open route         | Group routes by role in each `Mount`; add linter          |
| B-04  | P1 | `projects/`                       | `projects.Service` is 56 methods, 1647-line `services_ops.go`                    | Split into `projects` / `services` / `environments`       |
| B-05  | P2 | `handlers/github.go`              | Token-bucket + GC live in handler                                                | Move to `internal/ratelimit/installation.go`              |
| C-01  | P1 | `mcp/`                            | Pulls weight for deps but duplicates kusoclient + types                          | Import `api/apiv1` for shapes; keep its own transport     |
| C-02  | P1 | `coolify/`                        | Module promoted, but mapping/write side never followed                           | See A-04                                                  |
| C-03  | P2 | `api/apiv1/projects.go`           | `BoolPtr`/`IntPtr` aren't wire shape; pollute the contract module                | Move to `internal/ptr/` after apiv1 fills out             |
| C-04  | P2 | `cli/pkg/kusoApi/main.go`         | Shared `*resty.Request` mutated by every method — concurrent-call landmine       | Switch to `*resty.Client` + per-call `.R()`               |
| D-01  | P0 | `builds/` + helm-operator         | Cancel has to delete CR fields + Job + helm secrets to defang operator           | Replace helm-operator for KusoBuild with a Go controller  |
| D-02  | P1 | `builds/` annotations             | Phase/timing live on annotations; no shared schema                               | Wraps into D-01; otherwise extract `internal/buildmeta/`  |
| D-03  | P1 | `operator/helm-charts/*`          | Chart partial-validates fields server also validates; no contract               | Add `docs/CRD_VALIDATION_OWNERSHIP.md`                    |
| E-01  | P0 | See A-01                          | (Restated)                                                                       | (See A-01)                                                |
| E-02  | P1 | `web/src/types/projects.ts`       | No codegen — every type hand-mirrored from `kube/types.go`                       | Wire `tygo` against `api/apiv1` (after A-01)              |
| E-03  | P2 | `handlers/*` `writeJSON`          | Encode errors swallowed silently                                                 | Log at Warn level                                         |
| F-01  | P2 | `audit.Service` etc.              | 30+ `if h.Audit != nil` nil-guards; should be a no-op Service zero value         | Define `audit.NopService`, assign in main                 |
| F-02  | P2 | `KusoCron`                        | Three-shapes-in-one CRD; deferred from original review                           | Revisit before adding a 4th cron flavor                   |
