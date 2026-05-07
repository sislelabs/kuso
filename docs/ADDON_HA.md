# HA addons

`KusoAddon.spec.ha = true` switches an addon from "single-node
StatefulSet" to a replicated, failover-capable variant. As of v0.10:

| Engine | HA implementation | Replicas | Failover |
|---|---|---|---|
| postgres | CloudNativePG Cluster | 3 | automatic, ~30s |
| redis | sentinel mode | 3 | automatic |
| others | (no-op — falls back to single-node) | — | — |

This page is about the **postgres** path. CNPG is a one-shot operator
install. kuso doesn't manage CNPG itself because (a) it's a small
prerequisite that admins typically already have, and (b) shipping
yet-another-operator inside the kuso operator-of-operators is the
kind of complexity we said up front we wouldn't do.

## Prerequisites

Install CNPG once per cluster:

```bash
kubectl apply -f https://raw.githubusercontent.com/cloudnative-pg/cloudnative-pg/release-1.24/releases/cnpg-1.24.0.yaml
```

That registers the `Cluster`, `Backup`, `Pooler`, etc. CRDs and
deploys the operator to `cnpg-system`. ~150 MB image, ~64 Mi steady
state, no per-namespace footprint.

Verify with:

```bash
kubectl get crd clusters.postgresql.cnpg.io
kubectl -n cnpg-system get pod
```

Both should be present + Ready.

## Enabling HA on an addon

Set `ha: true` on the KusoAddon spec — that's the entire user-side
change:

```yaml
apiVersion: application.kuso.sislelabs.com/v1alpha1
kind: KusoAddon
metadata:
  name: my-project-pg
spec:
  project: my-project
  kind: postgres
  size: medium
  ha: true              # ← was false
```

Or via the UI: Addon settings → Replication → "High availability
(3-replica)". From the operator's side what changes:

- Single `StatefulSet` is replaced with a CNPG `Cluster` resource.
- 3 Postgres pods spin up (~60s on warm caches, ~3 min on cold).
- A `<addon>-rw` Service routes to the primary; `<addon>-ro` fans
  out to standbys.
- Our `<addon>-conn` Secret keeps the **same key set** —
  `DATABASE_URL` continues to point at the writable endpoint, so
  consumer services don't need a code change.
- The `READONLY_DATABASE_URL` key is added for apps that want to
  send read traffic to a replica.

## Operational notes

**Single-node clusters.** CNPG defaults to pod anti-affinity per
node — on a single-node cluster the 2nd and 3rd replicas will hang
in `Pending`. For dev / staging accept the SPOF with
`spec.singleNode: true`:

```yaml
spec:
  ha: true
  singleNode: true   # 3 replicas may colocate on the same node
```

**Failover.** Primary loss takes ~30s to detect + promote a standby.
Apps using a long-lived connection pool see `connection reset`
during the switchover. Use a driver retry policy
(`pgx.WithReconnectOnError` / pgbouncer / your ORM's "retry on
disconnect") to ride through cleanly. Apps that already do per-
request connection acquisition won't notice.

**Backups.** CNPG ships its own `Backup` CRD that uses
`barman-cloud-backup` to S3 / GCS / local PVC. The legacy kuso
`pg_dump` cronjob is *suppressed* in HA mode. Configure CNPG-native
backups out-of-band via:

```yaml
apiVersion: postgresql.cnpg.io/v1
kind: ScheduledBackup
metadata:
  name: my-project-pg-daily
  namespace: my-project
spec:
  schedule: "0 2 * * *"
  cluster:
    name: my-project-pg
```

(Native plumbing for ScheduledBackup via KusoAddon.spec.backup is
on the roadmap; not in v0.10.)

**Storage.** Each replica gets its own PVC, sized via the same
`size: small|medium|large` mapping used by single-node addons (5/20/100
GiB). With 3 replicas your storage cost triples — plan accordingly.

**Switching from non-HA to HA on an existing addon.** This is **not
seamless today**. The single-node StatefulSet uses different storage
layout from CNPG's per-replica PVCs. Recommended flow:

1. `kuso project addon backup my-project-pg` — manual snapshot.
2. Set `ha: true` on the addon (this triggers a helm uninstall of
   the StatefulSet path + install of the CNPG Cluster path).
3. Restore the snapshot into the new HA cluster:
   `kuso project addon restore my-project-pg --from-snapshot=...`
4. Bounce dependent services so they pick up the new `-conn` Secret
   (the host name changes from `<addon>` to `<addon>-rw`).

We'll automate this in a future kuso version. Until then, treat the
choice as install-time.

## When NOT to use ha

- Single-node clusters where you don't care about replication.
  Cheaper to keep `ha: false` + take a daily backup.
- Tiny apps (< 100 RPS to the DB). The 3× replica overhead isn't
  worth it.
- Cost-sensitive deployments (3 PVCs, 3 pod-CPUs, full sync
  replication overhead).

For low-traffic projects the default `ha: false` is the right call —
one StatefulSet, one PVC, one process. HA is for production
workloads where a 30-second failover is the difference between
"users noticed" and "they didn't" — and for anyone who'd otherwise
leave kuso for a managed Postgres and wants one less reason to.
