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

- [x] **#11 Settings nav grouping.** Re-bucketed the 18 settings routes
  into Cluster / Team / Integrations / You (was account/instance/admin).
  Group hint copy updated; locked-section keep-visible rule moved to
  the `team` group so non-admins still discover user-management.
- [x] **#12 Primary rollback affordance.** Added `HeaderRollbackChip`
  to `ServiceOverlay` header. Surfaces when the env is failed OR
  the most recent build failed; targets the most recent succeeded
  build with a relative-age hint. Inline yes/no confirm; reuses the
  existing `rollbackBuild` mutation.
- [x] **#13 Split `ServiceDeploymentsPanel.tsx` (527 → 237 LOC).**
  New `BuildRow.tsx` (330 LOC) holds the row + BuildErrorBanner +
  BuildLogs + RollbackButton + CancelButton + StatusBadge. The
  panel keeps the data hooks, env-branch filter, and the new slim
  `<BuildsList>` extraction.
- [-] **#14 Poll interval audit.** Skipped — re-audit found no per-
  node detail poll at 5s. The two 5s polls in `settings/nodes/view.tsx`
  are bootstrap-token watchers, which the original review item said
  should STAY at 5s. ProjectCanvas's 5s `latest-builds` poll has a
  matching 5s staleTime so refocus doesn't double-fetch. No action
  needed.
- [x] **#16 Truncate CHANGELOG.md.** Split at release #50 — recent
  releases stay in `CHANGELOG.md` (440 → ~440 LOC), older entries
  pushed to `CHANGELOG.archive.md`. `hack/release.sh` updated to
  regenerate via git-cliff into a tmp file then split + write both
  files on every ship; the boundary stays at the most-recent 50.
- [x] Bonus: stripped unused `Input` import from `welcome/page.tsx`
  (pre-existing lint error that was blocking CI).
- [x] Commit: `ux: settings grouping, rollback chip, deployments split, changelog cap`

## Phase 5 — Scalability (heavier)

- [x] **#6 Async notification webhook delivery.** New
  `NotificationOutbox` table + 10-worker drain pool. Dispatch path
  switched from in-memory channel fan-out (at-most-once) to
  outbox enqueue (at-least-once). Workers use FOR UPDATE SKIP
  LOCKED + a leader gate via the dispatcher's existing hook.
  Exponential backoff (5s → 5min cap) with ±20% jitter; dead-letter
  at 10 attempts. Daily prune sweeps delivered rows; dead-letter
  stays forever as audit trail. Tests: 4 DB-level (skip without
  `KUSO_TEST_PG_DSN`) + 2 pure-function backoff tests.
- [ ] **#7 Partition `log_lines` by day.** Convert to declarative
  partitioning. Migrate via schema bump. Prune becomes
  `DROP PARTITION`. New partitions cut on first insert via the
  existing daily cleanup tick. **Deferred** — non-trivial migration
  needs a session where it can be tested against real Postgres
  data.
- [ ] **#8 Node informer for watcher/sampler.** Switch `nodewatch`
  and `nodemetrics` from `List`-per-tick to a Node informer with
  event handlers. **Deferred** — half-day refactor of two
  goroutines; land in its own session.
- [ ] **#9 Build status SSE.** Replace the 5s poll in
  `ProjectCanvas`/`useBuilds` with SSE. **Deferred** — hardest
  item in the phase; touches server + client + needs replay/resume
  semantics for refresh. Half-day minimum.
- [x] **#10 PgBouncer in deploy bundle.** ConfigMap + Deployment +
  Service in `deploy/postgres.yaml`. DSN-stamp Job prefers
  `kuso-pgbouncer:6432`, falls back to direct rw Service when
  PgBouncer is absent. Pre-flip audit of DB usage confirmed
  transaction-pool compatibility.
- [x] **#15 instancepg health probe.** Periodic `SELECT 1` in
  Reconcile stamps `healthSnapshot{ok, err, checkedAt}` under
  `healthMu`. GetStatus reads it and flips Phase ready →
  unhealthy on failure (zero snapshot stays at "ready" to avoid
  flickering on fresh-leader first tick). Tests pin the three
  transitions + the probe-against-bad-DSN path.

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
