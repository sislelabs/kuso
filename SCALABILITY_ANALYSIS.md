# Kuso Scalability & Bottleneck Analysis

**Last reviewed:** v0.9.35 · **Profile:** multi-node Kubernetes PaaS, multi-replica control plane, Postgres-backed.

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
| SQLite single-writer ceiling | **Fixed in v0.9.** Postgres is now the control-plane DB. Multi-replica writes; pool of 25 conns. | `server-go/internal/db/db.go`, `deploy/postgres.yaml` |
| `kuso-server` pinned to one replica | **Fixed in v0.9.** `RollingUpdate` strategy, Lease-based leader election for singleton workers (build poller, alert engine, nodewatch, daily cleanup). | `deploy/server-go.yaml`, `internal/leader/` |
| RWO PVC blocked rolling deploys | **Fixed in v0.9.** State moved to Postgres; `kuso-server` is stateless above the database. | `deploy/server-go.yaml` |
| No informer cache; every handler `List`s the API server | **Fixed in v0.8.1.** Shared informer over the 6 CRDs, falls back to live API on cache miss. | `server-go/internal/kube/cache.go` |
| Backup endpoint required `KUSO_BACKUP_ENABLED=1` | **Fixed in v0.8.3.** Enabled by default; `KUSO_BACKUP_DISABLED=1` to opt out. | `server-go/internal/http/handlers/backup.go` |
| Install script regenerated admin password / JWT secret on re-run | **Fixed in v0.7.49.** Re-run reuses existing `kuso-server-secrets` values. | `hack/install.sh` |
| Edits to running CRs had no documented blast-radius contract | **Documented in v0.7.49.** | `docs/EDIT_SAFETY.md` |

---

## Active bottlenecks (in order of risk)

### 1. Notification dispatcher queue overflow (silent data loss)

`notify.Dispatcher` is a 256-event buffered channel **per pod**; if the buffer fills, `Emit` drops on the floor with a warn-level log. The events are also written to Postgres so the bell-icon feed survives the drop, but the webhook fan-out (Slack, Discord, custom URLs) does not.

**Trigger:** GitHub webhook burst (100+ pushes/min) plus a slow webhook sink (e.g. Slack on a flaky connection — 8 s timeouts).

**Symptom:** Webhook subscribers see gaps; bell-icon feed remains complete.

**Mitigation worth doing:** Async pool of 10 workers with retry + a `failed_notifications` table for the queue tail. ~200 LOC. Bumps the dispatcher from "fire-and-forget" to "fire-then-eventually-deliver-or-give-up-loudly."

---

### 2. `log_lines` table growth

All pod logs are persisted to Postgres with no retention. A 10-line/sec app fills ~8.6 M rows/day; 100 such apps would push 100 GB/month into the metadata DB.

**Workaround in place:** `/api/log-export` for manual ship; logship config can route to external sinks (Loki / generic webhook).

**Mitigation worth doing:** Default 7-day rolling retention on `log_lines`, partitioned by day so prune is `DROP PARTITION` instead of `DELETE WHERE`. Operators who want long retention configure logship to ship to Loki.

---

### 3. Build poller / alert engine throughput at high project count

Build poller and alert engine are leader-elected singletons. They list every active build / alert rule per tick and dispatch work synchronously. Past ~500 active services with 10s alert checks the leader pod CPU climbs noticeably.

**Mitigation worth doing:**
- Shard the alert engine by hash bucket — N buckets, each Leader-elected independently. Lets multiple replicas split the work.
- Move build status from poll to event-driven (watch on KusoBuild).

Neither is urgent; leverage kicks in past the 500-service mark.

---

### 4. Node sampler / watcher scale (acceptable up to ~50 nodes)

`nodemetrics.Sampler` (5-min tick) and `nodewatch.Watcher` (30-sec tick) `List` all Node CRs and walk the result. Per-tick cost:

- 1 node: ~50 ms
- 10 nodes: ~150 ms
- 50 nodes: 500 ms – 1 s
- 100 nodes: ~2 s, occasional API throttling

**Mitigation worth doing:** switch the watcher to a Node informer (event-driven, zero per-tick `List`); batch the sampler inserts. Half-day refactor when the time comes.

---

### 5. Postgres connection ceiling

Default pool is 25 conns/replica; the bundled in-cluster Postgres has `max_connections=100`. With three `kuso-server` replicas + the operator + logship + addon pollers, 100 fills.

**Mitigation worth doing for serious deployments:** PgBouncer in front of Postgres (transaction pooling), or point `KUSO_DB_DSN` at managed Postgres with a higher connection limit. For installs with > 3 replicas this is the first thing to wire up.

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

1. **Postgres indices** — `Notification(type, ts DESC)`, `UserGroup(userId, groupId)`, `log_lines(service_id, ts DESC)`. 2–5× speedup, zero ops change.
2. **Batch `NodeMetric` inserts** — multi-row INSERT.
3. **30-day audit log retention via partition pruning** — bounded growth, faster scans.
4. **CRD round-trip golden-file test** — protects schema against silent break on upgrade. (Added in v0.8.3 — see `internal/kube/crds_test.go`.)

### Medium (days)

5. **Async notification delivery** — 10-worker goroutine pool + retry table. Closes data-loss gap from #1.
6. **`log_lines` partitioned + 7-day retention by default** — bounds DB growth. Closes #2.
7. **WebSocket build status** — replaces 2-sec polling with event-driven push. Eliminates a hot path.
8. **PgBouncer in deploy bundle** — transaction pooler in front of Postgres. Unblocks > 3 replicas.
9. **Sharded alert engine** — N buckets, leader-elected per bucket. Closes #3 for high project count.

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

Kuso scales horizontally inside a cluster: add nodes for capacity, add `kuso-server` replicas for control-plane throughput, run HA addons for data-tier resilience, and point at managed Postgres when the bundled instance isn't enough. The remaining bottlenecks are mostly write-path concerns at high event volume — async delivery, partitioned log retention, and a Postgres pooler — all incremental work, none blocking.

If you're a user reading this trying to decide whether kuso fits: it's the right shape for production workloads on Kubernetes. If you need multi-region active/active or an edge runtime, integrate Cloudflare in front and managed Postgres behind — kuso is the layer in between, not the whole stack.
