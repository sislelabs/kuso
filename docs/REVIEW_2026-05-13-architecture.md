# Architecture Review — kuso v0.10.0
**Date:** 2026-05-13

Ground rules respected: single-tenant scope, multi-tenancy out of scope, all listed pre-closed findings skipped. Citations are absolute paths with line numbers.

---

## P0 — Ship now (or stop sleeping well)

### P0-1 — `projects.Service` god struct is regressing not shrinking
**Cost:** L
The last pass flagged 56 methods over 9 files. Today it's **53 methods over 9 files** — count went down by 3 but file count is unchanged and the largest files have grown. `server-go/internal/projects/services_ops.go` is now 1670+ lines with 37 functions; `server-go/internal/projects/env_groups.go` is 950+ lines with 16 functions; `server-go/internal/projects/projects.go:32` shows the Service struct still owns: ns cache, per-service mutex GC, metrics singleflight, addon-resolver closures, secret-cleanup callback, addon-conn-secret callback, revision-recorder callback. Adding a feature now means touching this one struct definition AND grepping which of 53 methods you collide with.
**Recommendation:** Extract `projects/envgroup` and `projects/drift` as sibling packages that consume a small `projects.Reader` interface. Keep `projects.Service` to project + service CRUD and the per-service lock primitive. `propagateChangedToEnvs` becomes its own `projects/propagate` package — it has a single entrypoint and is the highest-leverage chokepoint in the codebase.

### P0-2 — `db.DB` is also a god struct, and nobody's flagged it yet
**Cost:** L
140 methods across 29 files all hanging off `*db.DB` (`server-go/internal/db/*.go`). Every handler takes `*db.DB` and gets access to alert_rules + build_logs + github + invites + log_lines + node_metrics + notifications + roles + ssh_keys + tokens + users + tenancy_cache + revisions + login_attempts + … Same problem as `projects.Service` but worse because every HTTP handler holds a reference and the LSP "find usages on Method" is useless on a 140-method receiver.
**Recommendation:** Split into named repositories: `db.Users`, `db.Tokens`, `db.Notifications`, `db.Alerts`, etc., each holding `*sqlx.DB` and exposing only its own concern. Handlers take only the slice they need. Bonus: makes test stubs ~10x smaller because you stub one interface, not 140.

### P0-3 — `builds/builds.go` is a 2800-line single file
**Cost:** M
`server-go/internal/builds/builds.go` has 38 methods on `*Service` + `*Poller` mixed in one file (1-2800). Poller (`Run`, `tick`, `dispatchQueued`, `promoteOne`, `checkBuild`, `archiveLogs`, `mark*`, `promoteImage`) is conceptually a separate component from the request-handling `Service` (`Create`, `Cancel`, `Rollback`, `List`). They share state but that's not a reason to share a file.
**Recommendation:** Move Poller to `builds/poller.go`, archiveLogs to `builds/archive.go`, the admit/concurrency cap to `builds/admit.go`. The CR-shape helpers (`buildCRName`, `shortRef`, `splitGithubURL`) become `builds/refs.go`. Pure mechanical move; no semantic change.

---

## P1 — Important, plan-this-quarter

### P1-1 — Handlers reach into `s.Kube.Dynamic.Resource(...).List` 14 times, bypassing the typed/cached path
**Cost:** S
Found 14 call sites under `server-go/` that hit `Dynamic.Resource(...).Namespace(...).List` directly instead of going through the typed `kube.List*` helpers that consult the informer cache. Concrete hot paths affected:
- `server-go/internal/projects/projects_ops.go:246,389` (env list in `propagateBaseDomain`, `listEnvsForProjectWithServices`)
- `server-go/internal/projects/services_ops.go:644,733,795,1486` (envs for rename, delete, detected-env, propagation)
- `server-go/internal/projects/drift.go:159` (drift, polled every 10s per open overlay)
- `server-go/internal/builds/builds.go:668,716,887` (recent-branch lookups, active-for-service)

The cache + `list[T]` helper in `kube/crds.go:47` was added explicitly for this; these call sites silently miss it.
**Recommendation:** Add `ListKusoEnvironmentsByLabels`, `ListKusoBuildsByLabels` to `kube/crds.go` mirroring the existing `ListKusoAddonsByLabels` and migrate all 14 sites. A 5-line change per call site, real per-tick savings on multi-user dashboards.

### P1-2 — Env CRs and Cron CRs have NO ownerReferences pointing at parent service
**Cost:** S
`server-go/internal/projects/services_ops.go:315` creates the production env without `OwnerReferences`. Same at line 540 for `AddEnvironment`. Cron creation at `server-go/internal/crons/crons.go:232` also stamps no owner. That's the entire reason `projects.Service.DeleteService` and `Project.Delete` (`projects_ops.go:282-346`) have to enumerate envs/addons/builds/crons by hand. The comment at projects_ops.go:271 acknowledges this directly: *"ownerReferences would be the right structural fix; until those land, hand-rolled enumeration is the gate."* Services already have project ownerRefs (services_ops.go:216) — the pattern is established and one-sided. Risk: every new child-CR kind needs another hand-enumeration branch in DeleteProject, easy to forget (see `kusocron` flagged below).
**Recommendation:** Stamp KusoService ownerReference on every KusoEnvironment + KusoCron + KusoBuild at create time (BlockOwnerDeletion=false to keep the same non-deadlocking semantics). Delete cascades collapse to one `DeleteKusoProject` call and the hand-rolled sweep code in `Delete` becomes documentation of "what kube-GC handles for us".

### P1-3 — `env_groups.go:CreateEnvGroup` is a 348-line god-method
**Cost:** M
`server-go/internal/projects/env_groups.go:268-615` is one function: validates input, gets project, lists services, lists addons, builds rename maps, runs three nested clone loops, computes anchor annotations, manages a rollback closure. The rollback closure captures four mutable slices and is invoked from six different error returns — exactly the shape that produces "partial rollback" bugs.
**Recommendation:** Extract the four phases (`planClone`, `cloneAddons`, `cloneServices`, `cloneEnvs`) into private methods on a `envGroupCreate` value-struct that holds the accumulated state. Rollback becomes a single Method on that value. Adds ~30 lines but eliminates the implicit-state-capture footgun and makes the rollback path testable in isolation.

### P1-4 — KusoCron's three-shapes-in-one CRD is now paying rent
**Cost:** M
`server-go/internal/kube/types.go:502-547` shows KusoCronSpec carrying `Kind` ∈ {service, http, command} plus shape-specific fields (Service, URL, Image) — each only valid for a subset of Kinds. `server-go/internal/crons/crons.go` has the cost: separate `CreateCronRequest` vs `CreateProjectCronRequest`, separate `UpdateCronRequest` vs `UpdateProjectCronRequest`, separate `Add` vs `AddProject`, separate `Delete` vs `DeleteProject`, separate `Update` vs `UpdateProject`. Comment at types.go:511 admits the empty-Kind back-compat for pre-v0.8 crons. Two flavours now have separate APIs, separate validation, separate naming conventions (`<project>-<svc>-<short>` vs `<project>-<short>`).
**Recommendation:** Either commit to one shape (collapse Service-kind into Command-kind with auto-resolved image from production env, killing the special case) or split into two CRDs (KusoCron + KusoServiceCron). The current "one CRD, two parallel APIs" is the worst of both worlds — schema doesn't constrain anything and the handler code carries the cost of pretending it does.

### P1-5 — Migration package has zero tests
**Cost:** S
`server-go/internal/migration/migration.go` is the Coolify import service, freshly extracted (~240 lines per the brief). No `*_test.go` file. A bad import is catastrophic to a user — they're committing 50 services to their cluster. The only safety net is the wizard preview, which uses the same code path. Importer regressions are silent.
**Recommendation:** Add table-driven tests for `groupPicked`, `importOneProject`, and the slug-collision pathway. The classify + mapping side already has `coolify/mapping_test.go`; mirror the same shape here. Two days of work, eliminates the loudest unknown.

### P1-6 — Cron package has zero tests despite 3-shape complexity
**Cost:** S
`server-go/internal/crons/crons.go` — 14 methods, 490 lines, manages CR creation across three behavioural modes (service / http / command), inherits image+envFromSecrets+placement from production env, has its own schedule validator. No test file. Schedule validator regex (`cronExpr` at line 126) accepts `?` which is Quartz-only — kube CronJob rejects it, so the validation lies. Plus, `SyncFromService` (line 389) silently no-ops when the production env doesn't exist anymore.
**Recommendation:** Three table tests: (1) validateSchedule should reject `?` and `@hourly`; (2) Add() for each kind enforces only its required fields; (3) SyncFromService surfaces a clear error on missing-prod-env rather than the current "no error, no change" behaviour.

### P1-7 — `projects.Service.SetEnvWithOpts` swallows propagation errors silently
**Cost:** XS
`server-go/internal/projects/services_ops.go:944-949` returns `nil` on propagation failure with a `// Logged via the caller's wrapped error` comment that's a lie — no logger is invoked. The pre-existing comment at `PatchService` (line 1422) does the same: `_ = err` then return successful save. Combined with the lack of a logger on `*projects.Service`, this means: user saves env vars, kube write succeeds, env CRs silently don't get propagated, the running pod sees stale env. The drift report eventually surfaces it, but the immediate save-confirmation message says everything succeeded.
**Recommendation:** Plumb a `slog.Logger` field through `projects.Service` (already wired into the handler, just isn't passed down) and log propagation failures at WARN with project/service. Surface a non-fatal warning in the response body for the UI to render as "saved, propagation pending".

### P1-8 — Project CR Update path doesn't validate `Namespace` field changes
**Cost:** S
`server-go/internal/projects/projects_ops.go:163-231` is missing handling for `Namespace`. Once a project's spec.namespace is set at Create, there's no way to change it through Update — but Create only ensures the namespace exists once. If a project ever loses its execution namespace (deleted out-of-band), there's no recovery path through the API. Worse: the comment at projects.go:188 documents that the `nsCacheFor` lookup is the live-source-of-truth, and namespace cache invalidation only happens on update/delete, not on namespace-deleted-out-of-band.
**Recommendation:** Either (a) make spec.namespace immutable in the CRD schema and document it, or (b) plumb the namespace change through `Update` so a user can move a project's execution namespace. Status quo is "the field exists but isn't truly editable" which is the worst documentation outcome.

### P1-9 — Build poller's 5-second tick lists builds in EVERY namespace, every tick
**Cost:** M
`server-go/internal/builds/builds.go:1889` — every 5s, the poller iterates `ScanNamespaces(ctx)` and does a Dynamic.List per namespace for builds without the `done` label. On a single-tenant cluster with 1 namespace this is fine. On a 5-namespace cluster (a few projects each with their own ns) it's 5 list calls per 5s = 60 lists/min just for build polling — and these BYPASS the informer cache because they go through `Dynamic.Resource(GVRBuilds)` directly (line 1890). The informer at `kube/cache.go:107` already watches KusoBuild cluster-wide; the poller should consume it.
**Recommendation:** Replace the per-namespace List with a single cache.ListFromCache over GVRBuilds with the `!build-state` selector, then group by namespace in-memory. One call per tick instead of N.

### P1-10 — `propagateChangedToEnvs` is N kube writes per save (no parallelism, no batching)
**Cost:** S
`server-go/internal/projects/services_ops.go:1482` lists envs once then sequentially updates each. For a service with production + 3 preview envs, a single PatchService is 1 service-write + 4 env-writes = 5 sequential apiserver round-trips inside a request handler that's already on a 5s timeout (handlers/projects.go:251). Under operator churn the per-env Update can 409 (status patch race) and there's no retry — only `UpdateKusoServiceWithRetry` exists, not its env equivalent.
**Recommendation:** (a) Parallelise env updates via errgroup with a small fan-out (4 workers, since the typical N is < 20). (b) Add `UpdateKusoEnvironmentWithRetry` and use it on the propagation path.

### P1-11 — apiv1 is half-built: PATCH path still decodes into `projects.PatchServiceRequest` directly
**Cost:** M
`api/apiv1/services.go:37-42` explicitly admits `PatchServiceRequest is intentionally NOT in this file yet`. So the wire-stable contract covers POST (create) but not PATCH (update) — the most-used mutator. Same for the env-group surface, build trigger, addons update. Until apiv1 is closed, the CLI still hand-rolls every Patch shape and the same drift-class-of-bugs the module was built to prevent is alive on the modify path.
**Recommendation:** Either (a) finish apiv1 to cover every PATCH endpoint that has a stable shape (PatchService, PatchAddon, PatchEnv, PatchCron) or (b) commit to a smaller surface (only Create + Delete + small fixed paths) and document the rest as wire-unstable. The half-state is the worst.

### P1-12 — `migration/migration.go` takes concrete `*projects.Service` and `*addons.Service` instead of interfaces
**Cost:** XS
`server-go/internal/migration/migration.go:55-60` admits it inline: *"We don't take an interface for projects/addons because the only callers are real."* That's fine **until you want to test** — and as flagged above, migration has zero tests. The concrete dependency is the reason. The lazy "swap when mocking becomes useful" is now blocking tests on the freshly-extracted code.
**Recommendation:** Define `migration.Projects` + `migration.Addons` minimal interfaces (3-4 methods each, only what ImportCoolify actually calls). Real services satisfy them at no runtime cost; tests get a 50-line fake instead of a kube client.

---

## P2 — Low-priority polish

### P2-1 — `web/src/features/services/hooks.ts` polls `useEnvironments` every 10s in foreground
Combined with `useEnvGroups` (10s), `useServices` (no poll), `useBuilds` (10s), an open canvas tab issues 3 concurrent polls every 10s. Each describe call costs ~5 kube list reads (cached). With informer cache it's tolerable — but worth pricing into the scale envelope: 100 users × open canvas × 3 polls = 1800 reqs/min sustained just from idle canvases. `web/src/features/projects/hooks.ts:38,74` and `web/src/features/services/hooks.ts:62`.
**Recommendation:** Replace polling with a single SSE channel per project that pushes env/build/service change events. Existing notify dispatcher could do this; large change, but it kills three polls in one move.

### P2-2 — `coolify/` module sits alone in go.work but earns its keep
Read + write halves both moved here, mapping.go landed, has tests (`mapping_test.go`). This module **is** carrying its weight: shared by server, CLI, and the migration service; no kube deps. Healthy.

### P2-3 — `apiv1` module pulls its weight too despite half-finished surface
No imports outside stdlib (`api/apiv1/doc.go:13`), 4 files, used by both server handlers and CLI. The wire-shape sharing is real value. Just finish it (P1-11).

### P2-4 — `mcp` module is justified but tiny
8 Go files, 1 main, integration test exists. Stays a separate module because of the `github.com/modelcontextprotocol/go-sdk` dep — keeps that out of the server's go.sum. Healthy structural choice.

### P2-5 — `addons.Service.Add` immediately calls `RefreshEnvSecrets` synchronously
`server-go/internal/addons/addons.go:259` — after creating the addon CR, the request synchronously refreshes envFromSecrets on every service. With many services that's N writes inside the user's POST. Returning early and letting an event-driven refresh take over would cut tail latency on addon-add.
**Recommendation:** Move RefreshEnvSecrets to a background task triggered by the addon informer's Add event. Same code path, off the request thread.

### P2-6 — Stale `// FUTURE` block in `builds/builds.go` is now wrong
`server-go/internal/builds/builds.go:10-20` says *"this package + the helm-operator-driven Job rendering for KusoBuild are the most likely subsystem to move to a Go controller"* — but that move already happened (`buildcontroller/` exists, v0.10.0 brief confirms). Delete the comment, or rewrite to point to `buildcontroller` for new contributors.

### P2-7 — `projects/projects.go:62-66` mentions a "no-op invalidateDescribe()" left in for callsite compile compatibility
The brief says invalidateDescribe + facades.go are deleted, but the comment block describing why it was deleted is still there. Inert; just stale documentation. Trim.

### P2-8 — `KusoCronSpec` reuses chart fields `SuccessfulJobsHistoryLimit` + `FailedJobsHistoryLimit` but nothing in `crons.Service` exposes them through the API
`server-go/internal/kube/types.go:529-530` declares them; grep shows no handler reads them. Dead schema fields = either expose them or remove from the CRD shape on the next breaking version.

### P2-9 — `revoked_tokens` + `oauth_state` + `login_attempts` + `tokens` + `node_bootstrap_tokens` are all in `internal/db/` but they're auth concerns
The split is by storage layer, not by domain. Auth code in `internal/auth/` reaches across into 5 different db files to do its job. The god-`*DB` (P0-2) is the immediate problem; logically these tables belong to `auth` and should be private to it.

### P2-10 — Handler files in `internal/http/handlers/` aren't grouped — 52 files in one directory
`server-go/internal/http/handlers/` has 52 `.go` files: projects, services, addons, crons, builds, logs, secrets, alerts, ssh_keys, github, github_configure, kubernetes_*, node_bootstrap_*, audit, admin, settings, oauth, auth, invites, etc. Discoverability is poor; adding a feature means scrolling 50 file names. Group by domain: `handlers/projects/`, `handlers/auth/`, `handlers/cluster/`, `handlers/integration/`.

### P2-11 — `compareDeploymentToEnv` in `drift.go` does a live `Pods().List` per drift call
`server-go/internal/projects/drift.go:271` calls Clientset directly. The pod informer at `kube/cache.go:142` (with `ByLabelSelector`) would serve this in O(1). With the canvas polling drift every 10s for each open service overlay, this is the largest unmigrated hot-path-to-cache.
**Recommendation:** Use `c.ListPodsByLabel(sel)` instead.

### P2-12 — Tests live next to code but no `internal/migration` or `internal/crons` or `internal/buildcontroller` test files actually exist
buildcontroller has render_test.go (good), but the controller's `Run` loop (in `buildcontroller.go`) is untested. Three production-affecting controllers, two with zero coverage.

---

## Healthy bits worth calling out
- The Kube cache layer (`server-go/internal/kube/cache.go`) is well-designed: informer-backed, fallback-to-live, separate pod+deployment listers, podByNode index. The 14 places that **don't** use it (P1-1) are the bug; the cache itself is right.
- Per-service mutex + GC for delta-style edits (`server-go/internal/projects/projects.go:105-159`) is the correct pattern; it earned its keep on the AddDomain race.
- The schema-drift gate (router.go:720 + serverstate) is doing what it's supposed to.
- The build-controller extraction was the right call — the comments at `server-go/internal/buildcontroller/buildcontroller.go:1-42` honestly document why.
- apiv1 module structure (`api/apiv1/doc.go`) — no-deps rule + JSON-tag-as-contract is the right call.
- Coolify module promotion is paying for itself.
