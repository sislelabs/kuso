# Kuso Scalability & Bottleneck Analysis

**Last reviewed:** v0.8.3 · **Target profile:** single-box PaaS, 1–3 nodes, 10–100 projects, < 1000 pods.

This is not a roadmap to make kuso scale to thousands of projects — that's deliberately out of scope (see `CLAUDE.md` "Scope guardrails"). It's a reference for what limits the current architecture hits, what's already been done about them, and what the next reasonable mitigation would be if a user genuinely outgrows the design.

If you're considering kuso for a workload past the target profile, the answer is "use Render / Railway / Heroku / Fly." We say this in writing rather than bend the architecture.

---

## Architecture overview (current)

```
┌─────────────────────────────────────────┐
│   Next.js 16 Static SPA (embedded)      │  api() calls via JWT bearer
├─────────────────────────────────────────┤
│        Go HTTP Server (:3000)           │  chi router, JWT middleware
├─────────────────────────────────────────┤
│  Domain services                        │  projects, services, envs,
│  • projects.Service                     │  addons, builds, crons,
│  • builds.Service                       │  secrets, notifications,
│  • addons.Service                       │  nodewatch, nodemetrics,
│  • notify.Dispatcher (async, buffered)  │  github dispatcher
├─────────────────────────────────────────┤
│  Persistence                            │
│  ├ SQLite (/var/lib/kuso/kuso.db)       │  WAL mode, max-open-conns=1,
│  │   tenancy, auth, webhooks, audit,    │  busy-timeout 5s
│  │   metrics, notifications, invites    │
│  └ Kubernetes etcd (via k3s)            │  6 CRDs: Project, Service, Env,
│                                         │  Addon, Build, Cron
├─────────────────────────────────────────┤
│  Kube client                            │
│  • dynamic.Interface (CRDs)             │
│  • kubernetes.Interface (core)          │
│  • SHARED INFORMER CACHE over 6 CRDs ── │── reads served from local
│    (added v0.8.1, opt-in via            │   cache, watches keep it
│     EnableCache; server boots with it)  │   warm; writes go through
│                                         │   dynamic client unchanged
├─────────────────────────────────────────┤
│  Background goroutines                  │
│  • nodemetrics.Sampler (5 min tick)     │  metrics-server poll
│  • nodewatch.Watcher (30 sec tick)      │  NotReady cordon
│  • github.Dispatcher (webhooks + poll)  │
│  • notify.Dispatcher (256-buf chan)     │
│  • builds, crons, alerts (poll loops)   │
└─────────────────────────────────────────┘
```

---

## What's been mitigated since the original analysis

| Bottleneck | Status | Where |
| --- | --- | --- |
| No informer cache; every handler `List`s the API server | **Fixed in v0.8.1.** Shared informer over the 6 CRDs, falls back to live API on cache miss. | `server-go/internal/kube/cache.go` |
| Backup endpoint required `KUSO_BACKUP_ENABLED=1` to be flipped manually | **Fixed in v0.8.3.** Enabled by default; `KUSO_BACKUP_DISABLED=1` to opt out. | `server-go/internal/http/handlers/backup.go` |
| Install script regenerated admin password / JWT secret on re-run | **Fixed in v0.7.49.** Re-run reuses existing `kuso-server-secrets` values unless explicit env var overrides. | `hack/install.sh` |
| Edits to running CRs had no documented blast-radius contract | **Documented in v0.7.49.** UI enforcement is still TODO. | `docs/EDIT_SAFETY.md` |

---

## Active bottlenecks (in order of risk for a target-profile install)

### 1. SQLite single-writer concurrency (CRITICAL, by design)

**Why:** `db.SetMaxOpenConns(1)` is intentional — WAL doesn't help with write serialization at the application level, and the alternative (managing pool deadlocks across 30+ tables) is worse. Busy-timeout is 5s; requests that hit the lock wait then 409.

**When it bites:**
- Concurrent UI form submit + GitHub webhook on a busy project.
- Bulk invite mint + simultaneous build notification fan-out.
- Node-metrics flush coinciding with audit log write storm.

**Volume reference (single-node, 30-day window):**
```
NodeMetric             ~9k rows (5-min × 30d × 1 node)
NotificationEvent      capped at 200 rows (rolling)
Audit                  unbounded (~1–10 rows/sec under load)
log_lines              unbounded ← see #4 below
User + Group           < 50 rows typical
```

**Why we don't run Postgres:** Operational burden (separate container, backup story, dev/CI parity, schema migrations across two engines). For the target profile, SQLite + WAL + a 5s busy-timeout is enough — and we don't pretend otherwise.

**Cheap mitigations still on the table:**
- Index `Notification(type, ts DESC)` — speeds bell-icon fetch.
- Batch `NodeMetric` inserts — single multi-row INSERT instead of N.
- 30-day rolling-window audit log retention — bounds growth.

**When it's no longer enough:** > 10 concurrent admins, > 200 webhooks/min sustained, or audit-log scans crossing the 100-MB mark. Past that point Postgres is the answer; that also unlocks horizontal scaling, but neither is on the v0.x roadmap.

---

### 2. Horizontal scaling impossible (architectural, by design)

All durable state is instance-bound:
- SQLite is a local file.
- JWT_SECRET / KUSO_SESSION_KEY are pod env vars; tokens minted on pod-1 fail on pod-2.
- The `ProjectNamespaceResolver`'s 30 s TTL cache is in-memory.
- Build + cron pollers run independently; two replicas would both pick up the same job.

**The contract:** `replicas: 1` for `kuso-server`. The `Recreate` strategy on the deployment ensures no overlap during rolls.

**If the user needs HA, it's the wrong tool.** This is documented in the README.

---

### 3. Notification dispatcher queue overflow (silent data loss)

`notify.Dispatcher` is a 256-event buffered channel; if the buffer fills, `Emit` drops on the floor with a warn-level log.

**Trigger:** GitHub webhook burst (100+ pushes/min) plus a slow webhook sink (e.g. Slack on a flaky connection — 8 s timeouts).

**Symptom:** Bell-icon feed sparse, operator doesn't notice.

**Mitigation worth doing:** Async pool of 10 workers with retry + a small `failed_notifications` table for the queue tail. ~200 LOC. Bumps the dispatcher from "fire-and-forget" to "fire-then-eventually-deliver-or-give-up-loudly."

---

### 4. `log_lines` table growth (disk fills if not externally shipped)

All pod logs are persisted to a SQLite table with no retention. A 10-line/sec app fills ~8.6 M rows/day; 100 such apps would push 100 GB/month into the same file the rest of kuso writes to.

**Workaround in place:** `/api/log-export` for manual ship; logship config can route to external sinks (Loki / generic webhook).

**Mitigation worth doing:** Default 7-day rolling retention on `log_lines`, with a "logs older than 7 days are exported or gone" banner in the UI. Operators who want long retention can configure logship.

---

### 5. Node sampler / watcher scale (acceptable up to ~10 nodes)

`nodemetrics.Sampler` (5-min tick) and `nodewatch.Watcher` (30-sec tick) both `List` all Node CRs and walk the result. Per-tick cost:

- 1 node: ~50 ms
- 10 nodes: ~150 ms
- 50 nodes: 500 ms – 1 s

**Why it's OK at small scale:** Sampler is 5 min interval; even a 1 s tick is ~0.3% utilization. Watcher's 30 s tick × 1 s is 3% utilization — borderline.

**When it bites:** 50+ nodes, especially with kubelet scrape jitter or node churn (spot instances cordoning/uncordoning rapidly → rapid `notify.Event` flush → see #3).

**For the target profile this is a non-issue.** If a user really wants to run kuso on 50+ nodes, batch the inserts and switch the watcher to a Node informer. That's a half-day refactor when the time comes.

---

## Hottest paths (in order of frequency)

| Path | Frequency | What it does | Pre-cache | Post-cache |
| --- | --- | --- | --- | --- |
| `GET /api/projects` | every dashboard load + UI poll | List KusoProject CRs | live `List` 50–500 ms | cache hit < 5 ms |
| `GET /api/projects/{p}/services` | every overlay open + UI poll | List KusoService CRs filtered by project | live `List` 50–500 ms × N projects | cache hit, filter in memory |
| `GET /api/projects/{p}/services/{s}` (overlay) | every 5 sec while overlay open | List Service + Addon + Env CRs | 3× live `List` per tick | 3× cache hit |
| Build status poll | every 2 sec during active build | Get KusoBuild + Pod | live Get | live Get (intentional — needs current rv) |
| Webhook → notify → SQLite insert | per push / PR | DB write + dispatcher emit | DB-bound | DB-bound |
| `nodemetrics.Sampler` tick | every 5 min | metrics-server poll + N inserts | DB-bound | DB-bound |

The cache moved the read path from "every request hits the API server" to "every request hits an in-process map + the API server only on cache miss." For the target profile this is the difference between a snappy dashboard and one that feels like it's polling over 3G.

---

## Mitigations still worth doing (ranked by leverage)

### Cheap (hours)

1. **SQLite indices** — `Notification(type, ts DESC)`, `UserGroup(userId, groupId)`. 2–5× speedup, zero ops change.
2. **Batch `NodeMetric` inserts** — multi-row INSERT. Frees write lock sooner.
3. **30-day audit log retention** — bounded growth, faster scans.
4. **CRD round-trip golden-file test** — protects schema against silent break on upgrade. (Added in v0.8.3 — see `internal/kube/crds_test.go`.)

### Medium (days)

5. **Async notification delivery** — 10-worker goroutine pool + retry table. Closes data-loss gap from #3.
6. **`log_lines` 7-day retention by default** — bounds disk growth, keeps the SQLite file small. Closes #4.
7. **WebSocket build status** — replaces 2-sec polling with event-driven push. Eliminates a hot path entirely.
8. **Cache warm-up on boot (optional)** — `WaitForSync(ctx)` before serving the first request, gated on a flag. Marginal benefit; mostly a "first-request feels instant" polish.

### Out of scope for v0.x

9. **Postgres migration.** Unblocks horizontal scaling and removes the single-writer ceiling. Big lift; explicitly out of scope for the indie/single-box target.
10. **Multi-region / HA.** Off-roadmap. Documented in the scope guardrails.

---

## Measurement gaps

The right next step before adding more mitigations is **measuring what's actually slow**. Currently:

- No HTTP request latency histogram (chi middleware + `prometheus/client_golang` would do it in 30 LOC).
- No SQLite query timing wrapper.
- No kube-apiserver call latency tracking.

**Recommended metrics to add (when someone gets to it):**

```
kuso_http_request_duration_seconds{path, method, status}   histogram
kuso_db_query_duration_seconds{table, op}                  histogram
kuso_kube_list_duration_seconds{resource, cache_hit}       histogram
kuso_notification_queue_depth                              gauge
kuso_informer_synced{gvr}                                  gauge (0/1)
```

The kuso-prometheus deployment already runs in-cluster (see `deploy/prometheus.yaml`), so the scrape side is solved.

---

## Bottom line

Kuso is **well-designed for its narrow scope**. The remaining bottlenecks are either (a) intentional architectural trade-offs that match the target user, or (b) cheap-to-fix items waiting on someone hitting them. The shared informer cache (v0.8.1) was the biggest win on the read path; everything else on the list is incremental.

If you're a user reading this trying to decide whether kuso fits: 100 projects on 1–3 nodes is fine. 1000 projects is the wrong tool. The middle is where we'd want telemetry before guessing.
