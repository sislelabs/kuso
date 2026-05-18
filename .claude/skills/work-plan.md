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
- [x] **#7 Partition `log_lines` by day.** Opt-in via
  `KUSO_LOG_PARTITIONING=true`. New `db/log_partition.go` adds
  `LogPartitionState`, `EnsureLogPartitionForDay`,
  `EnsureLogPartitionWindow`, `PruneLogPartitionsBefore`, and
  `MigrateLogLineToPartitioned`. Daily cleanup now provisions the
  next-3-days window + drops past-retention partitions before
  falling through to the existing chunked DELETE (which no-ops on
  partitioned tables and continues to handle unpartitioned ones).
  Migration runs leader-only on first boot with the flag: rename
  legacy → create partitioned with `PRIMARY KEY (id, ts)` → copy
  in 100k-row batches → drop legacy + reseed BIGSERIAL. Tests:
  13-case pure parser for partition names + 4 DB-skippable
  integration tests (state probe, ensure no-op, prune no-op,
  migration round-trip). New `docs/LOG_PARTITIONING.md` carries
  the operator-facing maintenance-window procedure + rollback.
- [x] **#8 Node informer for watcher/sampler.** Added typed Node
  informer to `kube.Cache` alongside the existing Pod / Deployment
  informers. New `Cache.ListNodes()` returns a snapshot from the
  local indexer; `(nil, false)` triggers the cold-boot fallback to
  a live `Nodes().List()`. Both `nodewatch.Watcher.tick` and
  `nodemetrics.Sampler.sampleOnce` now prefer the informer path —
  on a 50-node cluster the ~500ms-per-30s-tick apiserver work goes
  to a microsecond map walk. Two new test paths pin the cache
  surface (nil-cache fallback + happy-path snapshot via
  `kubefake.NewSimpleClientset`).
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

- [x] **F2 KusoRun CR.** End-to-end shipped: CRD + helm chart +
  watches.yaml + Go types + GVR + CRUD + domain service + HTTP
  handler + CLI + phase-write poller + MCP `run` tool + UI Runs
  tab. The Runs tab in ServiceOverlay shows recent runs with phase
  pills and exposes an inline composer (command argv + KEY=VAL env
  overlay + timeout) for firing new runs. useRuns polls 3s while
  pending/running, 15s when settled — mirrors useBuilds. Cancel
  button shows for in-flight runs to services:write users.
- [~] **F1 Build dry-run.** Shipped: `KusoBuild.spec.dryRun` flag
  flows from CreateBuildRequest → CR → buildkit args. When true,
  buildkit uses `output=type=image,push=false` and skips
  `--export-cache`; the poller's markSucceeded short-circuits
  before promoteImage. CLI: `kuso build trigger --dry-run`. CRD
  schema + golden updated. **Deferred**: hadolint integration as a
  pre-buildkit init container.
- [x] **F3 Default-deny NetworkPolicy.** Flipped kusoproject's
  `networkPolicy.enabled` to true by default. The combined policy
  stack (default-deny + allow-dns + allow-intra-project +
  allow-platform + allow-registry + opt-in public-egress) was
  already in place; this just makes it on-by-default. **Deferred**:
  envref-derived fine-grained allow rules (A → only B vs.
  A → every sibling); needs live-cluster validation.
- [~] **F4 Preview-DB cloning.** Per-project addon clone path was
  already shipping pre-Phase-6 — the `previewdb.Cloner` creates a
  fresh `<source>-pr-<N>` addon CR and runs `pg_dump | psql` to
  seed it. **Deferred**: instance-pg case (`CREATE DATABASE` inside
  the cluster PG + dump/restore against the cluster's admin DSN);
  the addon loop now skips instance-pg addons with a loud warn log
  + a "preview shares source DB" hint so operators see the gap.
- [x] **F5 Cost rollup page.** New `db/cost_rollup.go` aggregates
  NodeMetric into per-(node, day) usage + per-node totals. New
  `/api/usage` handler + `/settings/usage` page renders the 7/30/90
  window picker + projection at operator-configured rates (spec.
  cost.{cpuPerHour, memGBPerHour, currency} on the Kuso CR).
  Per-project attribution explicitly deferred (NodeMetric is
  per-node; per-project needs a new sampler dimension).
- [x] **F6 MCP `plan` verb.** New `plan` tool that POSTs a kuso.yml
  to `/api/projects/{p}/apply?dryRun=1` and returns the typed
  diff. Read-only — callable from `--read-only` MCP mode via the
  new `kusoclient.PostRaw(... readOnlyOk=true)` helper. Text
  output mirrors `terraform plan` shape (`+ Services`, `~ Addons`).

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
