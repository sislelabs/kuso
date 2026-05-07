# Kuso Scalability & Bottleneck Analysis

**Last reviewed:** v0.9.38 · **Profile:** multi-node Kubernetes PaaS, multi-replica control plane, CloudNativePG-backed control-plane DB.

This document tracks where kuso's architecture bends under load, what's already been done about it, and what the next reasonable mitigation looks like. Treat it as a living reference — the bottlenecks shift as the platform grows.

kuso is built to scale up: add nodes, add `kuso-server` replicas, point at managed Postgres, run HA addons. Multi-region active/active and edge runtimes are deliberately **not** in scope — that's Cloudflare and managed-DB territory. Everything inside a single Kubernetes cluster, however, is fair game.

---

## Architecture overview (current)

```
┌─────────────────────────────────────────┐
│   Next.js 16 Static SPA (embedded)      │  api() calls via JWT bearer
├─────────────────────────────────────────┤
│   Go HTTP Server (multi-replica)        │  chi router, JWT middleware,
│   RollingUpdate strategy                │  Lease-based leader election
├─────────────────────────────────────────┤
│  Domain services                        │  projects, services, envs,
│  • projects.Service                     │  addons, builds, crons,
│  • builds.Service                       │  secrets, notifications,
│  • addons.Service                       │  nodewatch, nodemetrics,
│  • notify.Dispatcher (async, buffered)  │  github dispatcher
├─────────────────────────────────────────┤
│  Persistence                            │
│  ├ Postgres (kuso-postgres or external) │  pgx/lib-pq, pool 25,
│  │   tenancy, auth, webhooks, audit,    │  schema embedded, applied
│  │   metrics, notifications, invites    │  idempotently on boot
│  └ Kubernetes etcd (via k3s)            │  6 CRDs: Project, Service, Env,
│                                         │  Addon, Build, Cron
├─────────────────────────────────────────┤
│  Kube client                            │
│  • dynamic.Interface (CRDs)             │
│  • kubernetes.Interface (core)          │
│  • SHARED INFORMER CACHE over 6 CRDs ── │── reads served from local
│    (added v0.8.1)                       │   cache, watches keep it
│                                         │   warm; writes go through
│                                         │   dynamic client unchanged
├─────────────────────────────────────────┤
│  Background goroutines (leader-only)    │
│  • build poller                         │  Lease lock in
│  • alert engine                         │  coordination.k8s.io —
│  • nodewatch.Watcher (30 sec tick)      │  exactly one replica
│  • nodemetrics.Sampler (5 min tick)     │  runs each, the others
│  • daily cleanup                        │  stand by
├─────────────────────────────────────────┤
│  Stateless on every replica             │
│  • HTTP request handlers                │
│  • notify.Dispatcher (256-buf chan)     │  per-pod queue; events
│  • github webhook receiver              │  also persisted in Postgres
└─────────────────────────────────────────┘
```

---

## What's been mitigated

| Bottleneck | Status | Where |
| --- | --- | --- |
| **Cluster-wide `Pods("").List()` on every nodes endpoint hit (S1)** | **Fixed in v0.9.40.** Pod informer added to the shared cache with a `spec.nodeName` indexer; per-node pod count is now O(1) from the in-process index. | `server-go/internal/kube/cache.go`, `server-go/internal/http/handlers/kubernetes_nodes.go` |
| **`ListUserTenancy` Postgres JOIN per authorized request (S2)** | **Fixed in v0.9.40.** `ListUserTenancyCached` memoises the join for 60s per user; invalidations on group / role / membership writes (and `Invalidate*Tokens`) drop the entry. Eliminates the polling-driven join storm. | `server-go/internal/db/tenancy_cache.go`, `server-go/internal/http/handlers/{authz,projects,logs_ws}.go` |
| **Cache miss on every label-selected list (S3)** | **Fixed in v0.9.40.** `list[T]` parses the LabelSelector and applies it client-side against the shared informer indexer instead of falling through to a live API LIST. Every project-scoped path now reads from cache. | `server-go/internal/kube/{cache,crds}.go` |
| **`Describe` did O(envs) live `Deployments.Get` + per-env metrics-server List (S4)** | **Fixed in v0.9.40.** Deployment informer added to the shared cache; `aggregateCPUPercent` shares one ns-wide metrics list per 5s bucket via `singleflight`. 10-env project + open dashboard drops from ~22 round-trips/tick to 1. | `server-go/internal/kube/cache.go`, `server-go/internal/projects/{projects,projects_ops}.go` |
| **Build poller's cluster-wide `Pods("").List()` per admission + active-check (S5)** | **Fixed in v0.9.40.** Both `countRunningBuildPodsCluster` and `countActiveBuildsForProject` read from the Pod informer; live LIST is only the cold-cache fallback. | `server-go/internal/builds/builds.go` |
| **`archiveLogs` blocks the build poller when many builds finish in one tick (S6)** | **Fixed in v0.9.40.** Archive snapshots run on a bounded 4-worker pool (64-deep queue); poller continues immediately after queueing. Synchronous fallback on overflow keeps behaviour intact. | `server-go/internal/builds/builds.go` |
| **`LogLine.line ILIKE '%query%'` sequential scan in alert engine (S7)** | **Fixed in v0.9.40.** `pg_trgm` GIN index on `LogLine.line` + 24h server-side window cap on `CountLogMatches`. Five log-match alert rules at 60s no longer scan the whole partition each. | `server-go/internal/db/{schema.sql,log_db.go}` |
| **`InsertLogLines` did N round-trips per batch (S8)** | **Fixed in v0.9.40.** Single multi-VALUES INSERT per batch (chunked at 5000 rows for the parameter limit). Logship throughput improves ~50× on round-trip-bound paths. | `server-go/internal/db/log_db.go` |
| **Webhook fan-out duplicated per replica (S9)** | **Fixed in v0.9.40.** Dispatcher exposes `SetLeaderHook`; main wires it to a leader-elected `atomic.Bool` so multi-replica installs deliver each event to webhooks once. Bell-icon persistence still runs on every replica (DB unique constraint dedups). | `server-go/internal/notify/notify.go`, `server-go/cmd/kuso-server/main.go` |
| **Slow-loris on `ProjectsHandler.Apply` (S10)** | **Fixed in v0.9.40.** Replaced hand-rolled chunked reader with `io.ReadAll(io.LimitReader(...))`; honours `r.Context().Done()` and the 1 MiB cap before append. | `server-go/internal/http/handlers/projects.go` |
| **`projects.svcMutexes` leaked one mutex per (project, service) ever seen (S11)** | **Fixed in v0.9.40.** `RunServiceLockGC` mirrors the v0.9.38 fix in builds.Service: lastAccess timestamp + 15-min ticker + TryLock probe. | `server-go/internal/projects/projects.go` |
| **Cluster-wide Ingress / StorageClass lists hit kube-apiserver on every poll (S12)** | **Fixed in v0.9.40.** 30s in-process TTL cache on `Domains` and `StorageClasses`. | `server-go/internal/http/handlers/kubernetes_cluster.go` |
| **nodewatch lost NotReady-since on leader handover** | **Fixed in v0.9.40.** Persists `kuso.sislelabs.com/notready-since` annotation on first NotReady; new leader recovers state from the annotation instead of resetting the 5-min timer. | `server-go/internal/nodewatch/nodewatch.go` |
| **Bundled Postgres SPOF (control-plane node pinned, local-path PVC)** | **Fixed in v0.9.38.** Replaced with a CloudNativePG-managed 3-instance Cluster + pod anti-affinity + automatic failover. Operator installed by `hack/install.sh`. Single-node dev installs opt in via `KUSO_POSTGRES_SINGLE_NODE=true`. | `deploy/postgres.yaml`, `hack/install.sh` |
| **Helm-operator failures invisible to users** | **Fixed in v0.9.38.** `DriftReport` extracts the helm release-failed condition from the env CR's `.status` and renders it as a red chip in `ServiceOverlay`. The single biggest "platform feels broken" failure mode is now traceable from the UI. | `server-go/internal/projects/drift.go`, `web/src/components/service/ServiceOverlay.tsx` |
| **Operator over-privileged with `cluster-admin`** | **Fixed in v0.9.38.** Explicit `ClusterRole` listing only the verbs the helm-operator's charts emit; CNPG, cert-manager, and helm-shim resources whitelisted. | `deploy/operator.yaml` |
| **Tokens unrevocable until natural expiry** | **Fixed in v0.9.38.** JWTs carry a JTI; auth middleware probes `RevokedToken` (per-jti) + `UserTokenInvalidation` (per-user watermark). Logout, role demotion, group changes, password rotation, deactivation all bump the watermark — old tokens die on next request. | `server-go/internal/auth/middleware.go`, `server-go/internal/db/revoked_tokens.go`, `server-go/internal/http/handlers/{auth,users,roles,groups}.go` |
| **GitHub webhook replay + leak amplification** | **Fixed in v0.9.38.** X-GitHub-Delivery dedup table (24h retention) + per-installation token bucket (60/min). Leaked secret can no longer drive unbounded preview-env spam. | `server-go/internal/http/handlers/github.go`, `server-go/internal/db/github_webhook.go` |
| **`/metrics` public by default** | **Fixed in v0.9.38.** Admin-gated by default; `KUSO_METRICS_PUBLIC=true` to opt out. | `server-go/internal/http/router.go` |
| **No CSP / X-Frame-Options / HSTS on the SPA** | **Fixed in v0.9.38.** Headers applied to every HTML response (CSP, X-Frame-Options DENY, nosniff, HSTS, Referrer-Policy, Permissions-Policy). | `server-go/internal/spa/spa.go` |
| **Audit log gaps on destructive ops** | **Fixed in v0.9.38.** `project.delete`, `service.delete`, `addon.create` now audit-logged. Role/group mutations also call `Invalidate*Tokens` so the audit trail and security state stay consistent. | `server-go/internal/http/handlers/{projects,addons,roles,groups}.go` |
| **`LogLine` prune locks the table** | **Fixed in v0.9.38.** Chunked `DELETE WHERE ctid IN (...LIMIT 50000)` so the inserter never blocks for more than one batch. | `server-go/internal/db/log_db.go` |
| **`NotificationEvent` + `Audit` prune anti-pattern** | **Fixed in v0.9.38.** Switched from `WHERE id NOT IN (LIMIT N)` to `< (SELECT id LIMIT 1 OFFSET N)` — Postgres can stop after N rows of the descending PK b-tree instead of materialising the full keep-set. | `server-go/internal/db/notification_events.go`, `server-go/internal/audit/audit.go` |
| **m:n pivot tables only indexed on the right side** | **Fixed in v0.9.38.** `_UserToUserGroup_A`, `_PermissionToRole_A`, `_PermissionToToken_A` indexes added; auth checks no longer full-scan. | `server-go/internal/db/schema.sql` |
| **Backup/restore was a 501 stub** | **Fixed in v0.9.38.** `/api/admin/backup` streams gzipped pg_dump in-process; `/api/admin/restore` accepts the same shape, runs a one-shot Job that pipes through psql, and auto-rolls `kuso-server` so connection state stays consistent. CLI wraps both with progress polling. | `server-go/internal/http/handlers/backup.go`, `cli/cmd/kusoCli/backup.go` |
| **Registry filled silently with orphan blobs** | **Fixed in v0.9.38.** Weekly `garbage-collect` CronJob with scoped RBAC; pauses the registry, runs `registry garbage-collect --delete-untagged`, scales back up. | `deploy/registry.yaml` |
| **Settings page sprawl (16 cards, no search)** | **Fixed in v0.9.38.** Added searchable, keyword-tagged settings index with browse-mode group sections. | `web/src/app/(app)/settings/page.tsx` |
| **401 redirect lost form state** | **Fixed in v0.9.38.** `query-client.tsx` snapshots inputs/textareas/contenteditable to `sessionStorage` keyed on route; new `restoreFormDraft()` helper for opt-in restore on the post-login page. | `web/src/lib/query-client.tsx` |
| **Cookie `Secure` flag missed proxy-terminated TLS** | **Fixed in v0.9.38.** Honors `X-Forwarded-Proto: https` across all set-cookie sites (auth, invites, oauth). | `server-go/internal/http/handlers/{auth,invites,oauth}.go` |
| **PDB blocked drain on single-replica installs** | **Fixed in v0.9.38.** Switched to `maxUnavailable=1` (scales correctly past 1 replica) + annotation hint for single-replica operators. | `deploy/server-go.yaml` |
| `serviceLocks` map leaked entries | **Fixed in v0.9.38.** lastAccess timestamp + 15min GC ticker drops idle entries (preview-env churn no longer leaks). | `server-go/internal/builds/builds.go` |
| Build settings cache stale by up to 30s | **Fixed in v0.9.38.** Settings handler invalidates the in-memory cache on write so the next Create picks up new memory limits immediately. | `server-go/internal/http/handlers/settings.go` |
| `ConnMaxLifetime` tail of stale connections post-Postgres-restart | **Fixed in v0.9.38.** Lowered 30m→5m so the post-restart error wave clears in tens of seconds instead of tens of minutes. | `server-go/internal/db/db.go` |
| `PromoteUserToAdminIfNoAdmin` race | **Fixed in v0.9.38.** Wrapped in `pg_advisory_xact_lock` so two concurrent first-boot logins can't both promote. | `server-go/internal/db/bootstrap.go` |
| TEXT columns disk-fill footgun | **Fixed in v0.9.38.** CHECK constraints on `LogLine.line`, `ErrorEvent.{rawLine,message}`, `User.providerData`. | `server-go/internal/db/schema.sql` |
| CRD-version handshake | **Fixed in v0.9.38.** `KUSO_REQUIRE_CRDS=true` opts in to fail-closed startup if any required CRD is missing. | `server-go/cmd/kuso-server/main.go` |
| SQLite single-writer ceiling | Fixed in v0.9. Postgres is the control-plane DB. | `server-go/internal/db/db.go` |
| `kuso-server` pinned to one replica | Fixed in v0.9. `RollingUpdate` + Lease-based leader election. | `deploy/server-go.yaml`, `internal/leader/` |
| RWO PVC blocked rolling deploys | Fixed in v0.9. | `deploy/server-go.yaml` |
| No informer cache; every handler `List`s the API server | Fixed in v0.8.1. | `server-go/internal/kube/cache.go` |
| Notification dispatcher silent on overflow | Already had log+metric; the audit-agent flagged this as unfixed but the code emits `metricsDropped` + a warn log. Confirmed in v0.9.38. | `server-go/internal/notify/notify.go:254-265` |
| OAuthState single-statement update *was* atomic | The audit-agent's TOCTOU finding was based on a snapshot — `UPDATE … WHERE consumed=false` is atomic in Postgres. No fix needed. | `server-go/internal/db/oauth_state.go` |
| Placement save-time validation | Already implemented in `addons.validatePlacement` and `projects.validatePlacement`. No fix needed. | `server-go/internal/{addons,projects}/*.go` |
| `INSERT OR IGNORE` SQLite syntax | Already rewritten to Postgres `ON CONFLICT DO NOTHING` by `db.rewriteUpsert`. No fix needed. | `server-go/internal/db/db.go` |
| Install script regenerated admin password / JWT secret | Fixed in v0.7.49. | `hack/install.sh` |
| EDIT_SAFETY contract | Documented in v0.7.49. | `docs/EDIT_SAFETY.md` |

---

## Active bottlenecks (in order of risk)

### 1. `log_lines` table growth (mitigated, not eliminated)

All pod logs are persisted to Postgres with a 14-day default retention. A 10-line/sec app fills ~8.6 M rows/day; 100 such apps would push 100 GB/month into the metadata DB. v0.9.38 made the prune chunked (no more table-locking DELETEs) and added a per-row 16 KB CHECK, but the *write rate* is unchanged.

**Workaround in place:** `/api/log-export` for manual ship; logship config can route to external sinks (Loki / generic webhook).

**Mitigation worth doing:** Default 7-day rolling retention on `log_lines`, partitioned by day so prune is `DROP PARTITION` instead of `DELETE WHERE`. Operators who want long retention configure logship to ship to Loki.

---

### 2. Build poller / alert engine throughput at high project count

Build poller and alert engine are leader-elected singletons. They list every active build / alert rule per tick and dispatch work synchronously. Past ~500 active services with 10s alert checks the leader pod CPU climbs noticeably.

**Mitigation worth doing:**
- Shard the alert engine by hash bucket — N buckets, each Leader-elected independently. Lets multiple replicas split the work.
- Move build status from poll to event-driven (watch on KusoBuild).

Neither is urgent; leverage kicks in past the 500-service mark.

---

### 3. Node sampler / watcher scale (acceptable up to ~50 nodes)

`nodemetrics.Sampler` (5-min tick) and `nodewatch.Watcher` (30-sec tick) `List` all Node CRs and walk the result. Per-tick cost:

- 1 node: ~50 ms
- 10 nodes: ~150 ms
- 50 nodes: 500 ms – 1 s
- 100 nodes: ~2 s, occasional API throttling

**Mitigation worth doing:** switch the watcher to a Node informer (event-driven, zero per-tick `List`); batch the sampler inserts. Half-day refactor when the time comes.

---

### 4. Postgres connection ceiling

Default pool is 25 conns/replica; CNPG-bundled Postgres ships with `max_connections=100`. With three `kuso-server` replicas + the operator + logship + addon pollers, 100 fills.

**Mitigation worth doing for serious deployments:** PgBouncer in front of Postgres (transaction pooling), or point `KUSO_DB_DSN` at managed Postgres with a higher connection limit. For installs with > 3 replicas this is the first thing to wire up.

---

### 5. Notification dispatcher webhook fan-out reliability

The dispatcher persists every event to Postgres (so the bell-icon feed never drops), but the *webhook fan-out* path is best-effort with no retry-on-fail. A flaky Slack endpoint or expired Discord URL causes the dispatcher to drop the event with a warn log + a `kuso_notify_dropped_total` increment. The visibility piece is fixed; reliability isn't.

**Mitigation worth doing:** Async pool of 10 workers with retry + a `failed_notifications` table for the queue tail. Bumps the dispatcher from "fire-and-forget" to "fire-then-eventually-deliver-or-give-up-loudly." ~200 LOC.

---

## Hottest paths (in order of frequency)

| Path | Frequency | What it does | Cache hit |
| --- | --- | --- | --- |
| `GET /api/projects` | every dashboard load + UI poll | List KusoProject CRs | < 5 ms |
| `GET /api/projects/{p}/services` | every overlay open + UI poll | List KusoService CRs filtered by project | filter in memory |
| `GET /api/projects/{p}/services/{s}` (overlay) | every 5 sec while overlay open | List Service + Addon + Env CRs | 3× cache hit |
| Build status poll | every 2 sec during active build | Get KusoBuild + Pod | live Get (intentional — needs current rv) |
| Webhook → notify → DB insert | per push / PR | DB write + dispatcher emit | DB-bound |
| `nodemetrics.Sampler` tick | every 5 min | metrics-server poll + N inserts | DB-bound |

The shared informer cache moved the read path from "every request hits the API server" to "every request hits an in-process map." Postgres handles the write path comfortably at the volume a single cluster generates.

---

## Mitigations still worth doing (ranked by leverage)

### Cheap (hours)

1. **Batch `NodeMetric` inserts** — multi-row INSERT instead of N separate ones per tick.
2. **CRD round-trip golden-file test** — protects schema against silent break on upgrade. (Added in v0.8.3 — see `internal/kube/crds_test.go`.)

### Medium (days)

3. **Async notification delivery** — 10-worker goroutine pool + retry table. Closes the webhook-fan-out reliability gap.
4. **`log_lines` partitioned + 7-day retention by default** — bounds DB growth. Closes the largest remaining write-volume source.
5. **WebSocket build status** — replaces 2-sec polling with event-driven push. Eliminates a hot path.
6. **PgBouncer in deploy bundle** — transaction pooler in front of CNPG. Unblocks > 3 replicas.
7. **Sharded alert engine** — N buckets, leader-elected per bucket. Closes the high-project-count bottleneck.
8. **Node informer instead of List-per-tick** — `nodewatch.Watcher` + `nodemetrics.Sampler` switch from `List` per tick to event-driven. Half-day refactor.
9. **Wire CNPG WAL-archive backup by default** — `barmanObjectStore` against the same S3 bucket the kuso-postgres-conn admin already configured for the addon backup CronJob. Drops the metadata-DB RPO from "last manual `kuso backup`" to "last WAL segment" (~minutes).

### Bigger lifts

10. **Pluggable log backend.** Default to Loki instead of Postgres for `log_lines`. Removes the biggest unbounded write source from the metadata DB.
11. **Multi-region active/active.** Deliberately off-roadmap — we don't compete with Cloudflare on edge, and active/active for a stateful control plane is a different product.

---

## Measurement gaps

The right next step before adding more mitigations is **measuring what's actually slow**. Currently:

- No HTTP request latency histogram (chi middleware + `prometheus/client_golang` would do it in 30 LOC).
- No DB query timing wrapper.
- No kube-apiserver call latency tracking.

**Recommended metrics to add (when someone gets to it):**

```
kuso_http_request_duration_seconds{path, method, status}   histogram
kuso_db_query_duration_seconds{table, op}                  histogram
kuso_kube_list_duration_seconds{resource, cache_hit}       histogram
kuso_notification_queue_depth                              gauge
kuso_informer_synced{gvr}                                  gauge (0/1)
kuso_leader_elected{worker}                                gauge (0/1)
```

The kuso-prometheus deployment already runs in-cluster (see `deploy/prometheus.yaml`), so the scrape side is solved.

---

## Bottom line

Kuso scales horizontally inside a cluster: add nodes for capacity, add `kuso-server` replicas for control-plane throughput, run HA addons for data-tier resilience, and the bundled metadata DB is now CNPG-managed (3-instance, automatic failover) by default — the previous control-plane-node SPOF is gone. The remaining bottlenecks are mostly write-path concerns at high event volume — async webhook delivery, partitioned log retention, and a Postgres pooler — all incremental work, none blocking.

If you're a user reading this trying to decide whether kuso fits: it's the right shape for production workloads on Kubernetes. Single-node dev installs opt out of HA via `KUSO_POSTGRES_SINGLE_NODE=true`. If you need multi-region active/active or an edge runtime, integrate Cloudflare in front and managed Postgres behind — kuso is the layer in between, not the whole stack.
