---
name: work-plan
description: Active multi-session work plan — what's done, what's next, where the seams are. Read at the start of any session that resumes implementation work.
---

# Active work plan

This is the running plan for the platform-improvement series the user
asked for after the 2026-05-18 deep review. Each phase ships as one
or more commits. Phases are designed to be independently mergeable
— pick up where you left off by looking at the checklist below.

Treat the rule as "land one phase per session." Don't blow past a
phase boundary just because there's time on the clock — verify, commit,
stop.

## Status legend

- `[ ]` not started
- `[~]` in progress (commit half-landed, or follow-up still pending)
- `[x]` done and committed
- `[-]` dropped (with one-line reason)

---

## Phase 1 — Documentation cleanup

User asked to delete internal-review docs that "skew analysis and look
bad for users." Out: SCALABILITY_ANALYSIS.md, every REVIEW_2026-*.md,
REWRITE.md, REDESIGN.md, SCHEMA_MIGRATION.md, docs/superpowers/,
.claude/skills/projects-redesign.md, .claude/skills/crd-architecture.md.
Plus dangling `see SCALABILITY_ANALYSIS / REVIEW_2026 / REDESIGN` refs
in code/comments rewritten to standalone prose.

- [x] Delete review/scalability/design docs
- [x] Strip dangling refs from server-go, helm-charts, cli, mcp, web
- [x] Update .claude/skills/README.md + repo-orientation.md
- [x] Verify `go build ./...` passes
- [x] Commit: `chore: drop internal review/redesign docs`

## Phase 2 — Security P0s

The three security items that block scaling up trust in the platform.

- [x] **#1 RBAC: scope `secrets` writes per-namespace.**
  - ClusterRole `kuso-server` now: `secrets:[get,list,watch]` only;
    dropped cluster-wide `pods/exec`; `rolebindings:[get,list,create]`
    added so the stamper can bind into new namespaces.
  - New ClusterRole `kuso-server-managed-ns` carries
    `secrets:[create,update,patch,delete]` + `pods/exec:[create]`.
  - Static RoleBinding stamped into home `kuso` ns by the deploy
    bundle; `EnsureNamespace` stamps a binding into each project ns;
    `LabelNamespaceManaged` backfills for upgrade-in-place installs.
- [x] **#2 instancepg.Run leader gate.** Moved out of the always-on
  goroutine into `startSingletons` with a `KUSO_INSTANCEPG_DISABLED`
  env knob. Multi-replica installs no longer race on the admin DSN
  write.
- [x] **#3 instancepg SSL default.** New `coerceSSLMode` enforces
  `sslmode=require` as the default for non-local hosts and rejects
  explicit `sslmode=disable` for them. `buildAdminDSN` keys SSL off
  host class (loopback/in-cluster `.svc` → disable; everything else →
  require). New tests pin `coerceSSLMode`, `isLocalHost`, and the
  rewritten `buildAdminDSN` table.
- [x] Tests: `instancepg_test.go` updated with SSL coercion +
  in-cluster vs public-host policy. `go test ./...` green.
- [x] Commit: `fix(sec): scope secrets per-ns + instancepg ssl + leader gate`

## Phase 3 — Mechanical refactors (file splits, no behaviour change)

- [x] **#4 Split `builds.go` (2953 → 2007 LOC + 4 new files).**
  - `admission.go` — admit/cap/count helpers, supersedePriorBuilds,
    nsFor, ScanNamespaces, awaitPodGone, findRecent/findActive
  - `cards.go` — EventEnvelope, EnvelopeField, EventEmitter,
    buildRichCard, buildEventURL, lookupSiteURL, buildDurationMs,
    siteHostFromURL, isHexSHA, formatBuildDuration, event consts
  - `lifecycle.go` — Cancel, Rollback
  - `cache_pvc.go` — ensureCloneTokenSecret, ensureBuildCachePVC
  - `builds.go` keeps Service struct, New, List, Create (still the
    integration centre), Poller, and the build-status archive/promote
    machinery
  - Tests untouched — all in same package, no API changes.
- [x] **#5 `handlers.ProjectsAPI` interface seam.** New
  `projects_api.go` in handlers package lists the 32 methods
  handlers call. ProjectsHandler.Svc and ExportHandler.Projects
  switched from `*projects.Service` → `ProjectsAPI`. Compile-time
  guard (`var _ ProjectsAPI = (*projects.Service)(nil)`) catches
  signature drift. Tests can now stand up a fake without the kube
  + DB dependency chain.
- [x] Commit: `refactor: split builds.go + projects.Service interface seam`

## Phase 4 — UX wins (cheap)

- [ ] **#11 Settings nav grouping.** Group the 18 settings routes
  into Cluster / Team / Integrations / You sections in the left rail.
- [ ] **#12 Primary rollback affordance.** On `ServiceOverlay` header,
  when the current env is degraded OR the last build is failed, add a
  chip `Rollback to <build-name> (Xm ago)` next to the status pill.
  Calls the existing rollback endpoint.
- [ ] **#13 Split `ServiceDeploymentsPanel.tsx` (527 LOC).** Extract
  `<BuildRow>`, `<BuildErrorBanner>`, `<BuildLogsModal>`. No
  behaviour change.
- [ ] **#14 Poll interval audit.** Bump per-node detail poll on
  `settings/nodes/view.tsx:1185` from 5s → 15s. Keep the
  bootstrap-status poll at 5s.
- [ ] **#16 Truncate CHANGELOG.md.** Tail last 50 releases, archive
  rest to `CHANGELOG.archive.md`. Update `cliff.toml` if needed to
  cap output.
- [ ] Commit: `ux: settings grouping, rollback chip, poll cleanup`

## Phase 5 — Scalability (heavier)

- [ ] **#6 Async notification webhook delivery.** Two-table outbox
  + 10-worker pool with exponential backoff. New table
  `notification_outbox`. Dispatcher enqueues; workers drain. Bell
  feed unaffected (still goes through `NotificationEvent`).
- [ ] **#7 Partition `log_lines` by day.** Convert to declarative
  partitioning. Migrate via schema bump. Prune becomes
  `DROP PARTITION`. New partitions cut on first insert via the
  existing daily cleanup tick.
- [ ] **#8 Node informer for watcher/sampler.** Switch `nodewatch`
  and `nodemetrics` from `List`-per-tick to a Node informer with
  event handlers. ~half day.
- [ ] **#9 Build status SSE.** Replace 2-sec poll in
  `features/services/hooks.ts:107`-style with SSE over the existing
  notify event stream. Hardest of the bunch; touches both server
  and client.
- [ ] **#10 PgBouncer in deploy bundle.** Add transaction-pooler
  Deployment + Service to `deploy/postgres.yaml`. Wire `KUSO_DB_DSN`
  through it by default. Document opt-out for managed-DB users.
- [ ] **#15 instancepg health probe.** Wire periodic `SELECT 1` in
  `Reconcile` that stamps `LastError` and flips to `unhealthy` when
  ping fails. Makes the `unhealthy` phase reachable.

## Phase 6 — New features (big)

- [ ] **F2 KusoRun CR.** One-shot task runner. New CRD, new helm
  chart, new domain service, new HTTP/CLI/MCP surface. ~1-2 days.
- [ ] **F1 Build dry-run.** `KusoBuild.spec.dryRun = true` mode that
  hadolint-lints + checks base-image existence + first 3 stages.
  Smaller than F2 — bolt onto existing build pipeline.
- [ ] **F3 Default-deny NetworkPolicy + envref-derived allow rules.**
  Per-namespace policy at create-time. Allow rules synthesized from
  the `${{ X.URL }}` env-ref dependency graph.
- [ ] **F4 Preview-DB cloning.** For `instancepg.Mode == managed`,
  add `pg_dump | pg_restore` into preview env DB on PR open. Wire
  into `internal/previewdb`.
- [ ] **F5 Cost rollup page.** Aggregate `NodeMetric` + per-env
  metrics by project. New page `/settings/usage`. Settable
  `cost.cpuPerHour` in `Kuso` CR.
- [ ] **F6 MCP `plan` verb.** New MCP tool returning the kube-level
  diff for a desired-state spec without applying.

---

## Working rules

1. Land one phase per session. Verify with `go build ./...` and the
   relevant smoke test before committing.
2. Each commit message follows the existing conventional-commit shape
   (`fix(sec):`, `refactor:`, `ux:`, `feat:`, etc).
3. Don't release — user runs `make ship` manually.
4. If a phase needs the live cluster (e.g. testing RBAC scoping or
   PgBouncer), say so in the commit message and leave a
   verification checklist in `docs/LIVE_TEST_PLAN.md`.
5. If you have to skip an item, mark it `[-]` with one-line reason
   so the next session knows it was deliberate.
