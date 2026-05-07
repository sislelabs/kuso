# Backup & Restore

This is the canonical day-2 doc for protecting the kuso control-plane DB. If you are running kuso in production, **read this once and set up at least the daily snapshot path before you put anything important on the box**.

## What lives where

kuso has three distinct kinds of state. They have different recovery stories.

| State | Lives in | Lost if you lose it |
| --- | --- | --- |
| Control-plane data | Postgres (`kuso-postgres` StatefulSet, or external managed Postgres pointed at by `kuso-postgres-conn`) | Users, sessions, audit log, webhooks, invites, SSH keys, node metrics, notifications, GitHub App config, instance secrets, log_lines |
| Kubernetes-native state | etcd / k3s | All `KusoProject` / `KusoService` / `KusoEnvironment` / `KusoAddon` / `KusoBuild` / `KusoCron` CRs, plus every Deployment / Service / Ingress / Secret kuso ever created |
| Addon data | Per-addon PVC inside the addon's own StatefulSet (or CNPG cluster volumes for HA Postgres) | Postgres / MySQL / Redis / Mongo data |

**The Postgres metadata DB is the highest-leverage thing to back up.** The CRDs are recreatable from your repo + GitHub App config (which itself is in Postgres). The addon PVCs need their own backup story (see `kuso addon backup` below).

## Daily: snapshot the metadata DB

The `kuso` CLI ships with `backup` and `restore` verbs that pull / push a `pg_dump` snapshot via an admin-only HTTP endpoint. **Backup is enabled by default** since v0.8.3 — no extra config step. From your workstation:

```bash
kuso backup -o /backups/kuso-$(date -u +%Y%m%d).sql.gz
```

If you have a compliance reason to lock it down (multi-tenant kuso-as-a-service, regulated environment), set `KUSO_BACKUP_DISABLED=1` on the server deployment to remove the routes entirely:

```bash
kubectl -n kuso set env deployment/kuso-server KUSO_BACKUP_DISABLED=1
kubectl -n kuso rollout status deployment/kuso-server
```

Pipe it into whatever you already use — `restic`, `borg`, S3, `cron + scp`. Concrete cron example:

```cron
# /etc/cron.d/kuso-backup — runs daily at 04:00 UTC
0 4 * * * root kuso backup -o /var/backups/kuso/kuso-$(date -u +\%Y\%m\%d).sql.gz \
  && find /var/backups/kuso -mtime +30 -delete
```

The backup file contains **JWT secrets, hashed passwords, GitHub App private keys, and audit logs**. Treat it as a credential — encrypt at rest, restrict read access, don't commit to git.

### Off-host: ship the snapshot somewhere else

A backup on the same disk as the server doesn't survive a disk failure. Bare minimum: `rsync` / `aws s3 cp` / `restic backup` the snapshot to a different machine.

```bash
kuso backup -o /tmp/kuso.sql.gz
aws s3 cp /tmp/kuso.sql.gz s3://your-backups/kuso/$(date -u +%Y%m%dT%H%M).sql.gz
shred -u /tmp/kuso.sql.gz
```

### Why go through the CLI instead of `pg_dump`?

You can run `pg_dump` directly — see "manual fallback" below. The CLI path is preferred because it goes through the running server, which knows the canonical DSN and applies the right flags for a logically-consistent dump (`--no-owner --no-privileges --clean --if-exists`).

## Recovery paths

### Path A — the DB is fine, you just want to roll back to yesterday

```bash
kuso restore /backups/kuso-20260504.sql.gz
```

`kuso restore` streams the dump to the server which applies it inside a single Postgres connection. Existing connections from `kuso-server` replicas survive — the schema's `--clean --if-exists` shape drops + recreates tables; the next request hits fresh data.

A `kubectl -n kuso rollout restart deployment/kuso-server` afterwards is a good belt-and-braces step if you want to drop any in-memory caches that pre-date the restore.

### Path B — the DB is corrupted

Symptom: `kuso-server` pod failing with Postgres errors that don't match the schema, `kuso-postgres` pod CrashLoopBackOff, or a Postgres `data corruption` log line. Rare on a kernel that doesn't lose power, but it happens.

1. Stop `kuso-server` so nothing is writing while you work:
   ```bash
   kubectl -n kuso scale deployment kuso-server --replicas=0
   ```
2. Investigate the Postgres pod:
   ```bash
   kubectl -n kuso logs sts/kuso-postgres
   kubectl -n kuso exec -it sts/kuso-postgres -- psql -U kuso -d kuso \
     -c "SELECT datname, pg_database_size(datname) FROM pg_database;"
   ```
   If it's a recoverable error (e.g. a single index needs `REINDEX`), do that. If the data files are bad, restore.
3. Restore from the latest dump:
   ```bash
   kubectl -n kuso scale deployment kuso-server --replicas=1
   kubectl -n kuso rollout status deployment/kuso-server
   kuso restore /backups/last-good.sql.gz
   ```

If you're using external managed Postgres, point-in-time recovery from the provider is faster than a `kuso restore` — restore the cluster, then `kubectl rollout restart deployment/kuso-server`.

### Path C — the box is gone (disk failure, host loss)

You need a fresh `install.sh` run on a new box, then a `kuso restore` of the last good snapshot.

1. Provision a new host. Point your DNS at it (or update DNS first and wait for propagation).
2. Run `install.sh` exactly as you did the first time. The installer is idempotent on a fresh box; it'll build a clean cluster.
3. Before letting any user log in, restore the snapshot:
   ```bash
   kuso login --api https://kuso.example.com -u admin -p '<password from install summary>'
   kuso restore /backups/last-good.sql.gz
   ```
4. Wait for kuso to reconcile. The CRDs in your old cluster are gone; they need to be recreated. The fastest path is to re-import each project from its repo via the UI / CLI, then restore addon data from your addon backups (see below).

For warm-spare DR (faster than re-running install), point `kuso-postgres-conn` at managed Postgres with cross-region replication and snapshot the etcd / k3s state separately. Out of the box this isn't a one-button thing — you wire it up.

## Addon data

`kuso addon backup` triggers an addon-specific backup job (Postgres → `pg_dump`, Redis → `BGSAVE` + RDB copy, etc.) and uploads to whatever object store the addon is configured against. See `kuso addon backup --help`. Snapshot the metadata DB **and** the addons on the same schedule — restoring one without the other is a guaranteed bad time.

For HA Postgres addons (CNPG-backed `KusoAddon.spec.ha = true`), CNPG-native backups are the right path — see `docs/ADDON_HA.md`. The `pg_dump` cron is suppressed in HA mode.

## Manual fallback (no CLI access)

If `kuso` itself is broken and you need to grab the metadata DB by hand:

```bash
# On any host with kubectl + the kuso kubeconfig.
# CNPG creates pods named kuso-postgres-1, kuso-postgres-2, ...
# kuso-postgres-1 is typically the primary; kuso-postgres-rw
# Service always points at the current primary regardless.
#
# Easiest path: port-forward the rw Service, run pg_dump locally
# (postgresql-client must be installed):
kubectl -n kuso port-forward svc/kuso-postgres-rw 5432:5432 &
PGPASSWORD=$(kubectl -n kuso get secret kuso-postgres-conn -o jsonpath='{.data.password}' | base64 -d) \
  pg_dump -h localhost -U kuso --no-owner --no-privileges --clean --if-exists kuso \
  | gzip > kuso-snapshot.sql.gz
```

`pg_dump` is online-safe and produces a logically consistent snapshot.

## CNPG password divergence (rare)

In v0.9.38+ the bundled metadata DB is a CloudNativePG-managed Cluster.
The `kuso-postgres-conn` Secret holds the credentials kuso-server reads
to connect; the password authoritatively comes from CNPG's own
`kuso-postgres-app` Secret (created during `bootstrap.initdb`). The
`kuso-postgres-dsn-stamp` Job runs after every Cluster apply and copies
the CNPG-managed password into `kuso-postgres-conn` — so if anything
ever drifts, re-running the Job reconciles it.

Symptoms of divergence:
- kuso-server logs `pq: password authentication failed for user "kuso"`
- `install.sh` warns `kuso-postgres-conn.password diverges from kuso-postgres-app.password`

Recovery:

```bash
# Re-run the dsn-stamp Job — it overwrites kuso-postgres-conn with
# whatever CNPG actually has in kuso-postgres-app.
kubectl -n kuso delete job kuso-postgres-dsn-stamp
curl -sfL https://raw.githubusercontent.com/sislelabs/kuso/main/deploy/postgres.yaml | kubectl apply -f -
kubectl -n kuso wait --for=condition=Complete job/kuso-postgres-dsn-stamp --timeout=300s
kubectl -n kuso rollout restart deployment kuso-server
```

If the Cluster itself is broken (failed bootstrap, missing
`kuso-postgres-app` Secret, primary stuck `Pending`):

```bash
# 1. Delete the Cluster (NOT the PVCs — those carry your data).
kubectl -n kuso delete cluster kuso-postgres
# 2. Delete the now-stale credentials Secrets.
kubectl -n kuso delete secret kuso-postgres-conn kuso-postgres-app
# 3. Re-run install.sh; it'll mint fresh credentials and let CNPG
#    bootstrap a new Cluster against the existing PVCs.
```

This loses no data — CNPG's bootstrap re-attaches to existing PVCs.
Verify by running `kuso backup` afterwards and inspecting row counts.

## What we don't do (and probably won't)

- **Continuous WAL streaming.** Plausible future work — `wal-g` or managed-Postgres point-in-time recovery is the right shape. Not bundled today; daily snapshots + 30-day retention is the recommended baseline.
- **Automatic restore.** Every restore is a destructive operation; we want the human in the loop.
- **Cross-region active/active control plane.** Out of scope — point at a managed Postgres with cross-region replication if you need it; for k8s state you're on etcd snapshots + a runbook.
