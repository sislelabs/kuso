---
name: platform-architecture
description: Use when reasoning about kuso's runtime shape — control-plane scaling, persistence, HA, leader election, multi-node placement. The "what kuso actually is in production" reference.
---

# kuso platform architecture

kuso is a multi-node, multi-replica Kubernetes PaaS. The control plane is stateless above Postgres; data-tier addons can run HA; node placement is label-driven. This file is the source-of-truth for how the moving parts fit together.

## Control plane

```
kuso-server (Deployment, RollingUpdate, replicas ≥ 1)
    │
    ├── HTTP handlers (stateless)         every replica serves API + UI
    ├── informer cache                    every replica keeps a local copy
    ├── notify.Dispatcher (per-pod)       every replica fans out webhooks
    │
    └── leader-elected singletons         exactly one replica runs each:
        • build poller
        • alert engine
        • nodewatch.Watcher
        • nodemetrics.Sampler
        • daily cleanup
```

**Leader election** uses `coordination.k8s.io/Lease`. The ClusterRole grants `[get, list, watch, create, update, patch, delete]` on leases. Without these verbs the helper falls back to "always run" → multi-replica deploys double-promote builds and double-fire alerts.

**Stateless above the DB.** Sessions, JWTs, audit log, webhook config — everything durable lives in Postgres. The pod can be killed at any time; the next replica picks up requests immediately. `RollingUpdate` is the deploy strategy (replaces the v0.8 `Recreate` strategy that was forced by the RWO SQLite PVC).

## Persistence

| Store | Lives in | What it holds |
| --- | --- | --- |
| **Postgres** (`kuso-postgres` StatefulSet, or external) | RWO PVC if in-cluster | Users, sessions, audit, webhooks, metrics, notifications, log_lines, GitHub App config, instance secrets |
| **etcd** (via k3s) | etcd | All `Kuso*` CRs + Deployments / Services / Ingresses / Secrets we created |
| **Addon PVCs** | per-addon StatefulSet | Postgres / MySQL / Redis / Mongo data |

The bundled in-cluster Postgres is a single-replica StatefulSet co-located on the control-plane node. For serious deployments, replace `kuso-postgres-conn` Secret's `dsn` with a managed Postgres URI (RDS / Crunchy Bridge / Supabase) — `kuso-server` doesn't care.

`server-go/internal/db/db.go` has the connection-pool shape: `MaxOpenConns=25`, `MaxIdleConns=5`, idle-timeout 5 min, lifetime 5 min. Cap is per-replica; with three replicas and the operator + logship + addon pollers, the bundled Postgres `max_connections=100` ceiling is the next bottleneck — PgBouncer is the answer when you cross it.

## HA addons

`KusoAddon.spec.ha = true` switches an addon from single-StatefulSet to a replicated, failover-capable variant:

| Engine | HA shape | Replicas | Failover |
| --- | --- | --- | --- |
| postgres | CloudNativePG `Cluster` | 3 | automatic, ~30s |
| redis | Sentinel mode | 3 | automatic |
| others | (no-op — falls back to single-node) | — | — |

CloudNativePG is a one-shot operator install — kuso doesn't bundle it (would be operator-of-operators complexity). See `docs/ADDON_HA.md` for the prereq install + the no-go switch from non-HA to HA on an existing addon.

## Multi-node

Nodes join via token-based bootstrap (Settings → Nodes → Add node). The bootstrap token mints a single-use `curl … | sudo sh` one-liner; the agent installs k3s and registers itself. Works behind NAT.

**Auto-cordon on failure.** `nodewatch.Watcher` (leader-elected, 30 s tick) cordons nodes that have been NotReady > 5 min. Marker annotation `kuso.sislelabs.com/cordoned-by-nodewatch` so we only auto-uncordon nodes WE cordoned. Fires `node.unreachable` / `node.recovered` notify events on transition.

**Placement is label-driven.** `kuso.sislelabs.com/<key>` labels on nodes; AND-of-labels selectors on services and addons. The placement editor reconciles labels via `internal/projects.PlacementMatchesNode` (in `kube/types.go`). Bare keys without prefix are kuso-internal (`kuso.sislelabs.com/project`, `…/service`, `…/addon-kind`).

## Self-update

Live instances poll the GitHub releases endpoint and self-update via the in-built updater (`make ship` cuts the release; instances pull on the next tick). The updater swaps the `kuso-server` image, rolls the operator when applicable, and applies any new CRDs. **No ssh-from-laptop step.**

CRD schema changes still need `kubectl apply -f operator/config/crd/bases/...yaml` via ssh — the auto-updater only flips image tags. The operator helm-operator picks up CR spec changes via watch + 3 m reconcile.

## Hot read paths

The shared informer cache (`internal/kube/cache.go`) serves reads from a local in-process map — every `kuso-server` replica keeps its own copy, kept warm by watches. Cache miss falls through to a live `List`. This is the difference between a snappy dashboard and one that polls over 3G.

Writes go through the dynamic client unchanged — no write-side caching, no eventual-consistency surprises.

## When you're touching this layer

- **Adding a singleton background worker** → wire it through `internal/leader/`, not just a goroutine in main. Leader-elected workers must be idempotent across re-elections (lease ~15 s).
- **Adding a Postgres table** → add it to `server-go/internal/db/schema.sql`. The schema is applied idempotently on every boot; new tables are safe, schema changes need migration semantics.
- **Multi-replica safety** → if your handler holds in-memory state that must agree across replicas, the answer is Postgres or a leader-elected worker — not a sync.Map. The 30 s `ProjectNamespaceResolver` cache is fine because every replica recomputes it from the same kube state.
- **Connection budget** → don't open new long-lived DB connections per request. Use the shared pool.
