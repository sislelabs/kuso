# Backup & Restore

This is the canonical day-2 doc for protecting the kuso control-plane DB. If you are running kuso in production, **read this once and set up at least the daily snapshot path before you put anything important on the box**.

## What lives where

kuso has two distinct kinds of state. They have different recovery stories.

| State | Lives in | Lost if you lose it |
| --- | --- | --- |
| Control-plane data | SQLite at `/var/lib/kuso/kuso.db` inside the `kuso-server` pod, backed by the `kuso-server-go-data` PVC (RWO, local-path) | Users, sessions, audit log, webhooks, invites, SSH keys, node metrics, notifications, GitHub App config, instance secrets |
| Kubernetes-native state | etcd / k3s | All `KusoProject` / `KusoService` / `KusoEnvironment` / `KusoAddon` / `KusoBuild` / `KusoCron` CRs, plus every Deployment / Service / Ingress / Secret kuso ever created |
| Addon data | Per-addon PVC inside the addon's own StatefulSet | Postgres / MySQL / Redis / Mongo data |

**The SQLite DB is the highest-leverage thing to back up.** The CRDs are recreatable from your repo + GitHub App config (which itself is in SQLite). The addon PVCs need their own backup story (see `kuso addon backup` below).

## Daily: snapshot the SQLite DB

The `kuso` CLI ships with `backup` and `restore` verbs that pull / push the SQLite file via an admin-only HTTP endpoint. To enable, set on the server:

```bash
kubectl -n kuso set env deployment/kuso-server KUSO_BACKUP_ENABLED=1
kubectl -n kuso rollout status deployment/kuso-server
```

Then from your workstation:

```bash
kuso backup -o /backups/kuso-$(date -u +%Y%m%d).sqlite
```

Pipe it into whatever you already use — `restic`, `borg`, S3, `cron + scp`. Concrete cron example:

```cron
# /etc/cron.d/kuso-backup — runs daily at 04:00 UTC
0 4 * * * root kuso backup -o /var/backups/kuso/kuso-$(date -u +\%Y\%m\%d).sqlite \
  && find /var/backups/kuso -mtime +30 -delete
```

The backup file contains **JWT secrets, hashed passwords, GitHub App private keys, and audit logs**. Treat it as a credential — encrypt at rest, restrict read access, don't commit to git.

### Off-host: ship the snapshot somewhere else

A backup on the same disk as the server doesn't survive a disk failure. Bare minimum: `rsync` / `aws s3 cp` / `restic backup` the snapshot to a different machine.

```bash
kuso backup -o /tmp/kuso.sqlite
aws s3 cp /tmp/kuso.sqlite s3://your-backups/kuso/$(date -u +%Y%m%dT%H%M).sqlite
shred -u /tmp/kuso.sqlite
```

### Why not `sqlite3 .backup`?

You can — see "manual fallback" below. The CLI path is preferred because it goes through the running server, which holds a write lock on the WAL and knows how to take a consistent snapshot. Direct file copies of an active SQLite DB can race with WAL checkpoint and produce a torn snapshot.

## Recovery paths

### Path A — the DB is fine, you just want to roll back to yesterday

```bash
kuso restore /backups/kuso-20260504.sqlite
kubectl -n kuso rollout restart deployment/kuso-server
```

The pod restart is **required** — the running server holds an open `*sql.DB` against the pre-swap file, and `restore` swaps the file on disk without touching the live process.

### Path B — the DB is corrupted

Symptom: `kuso-server` pod CrashLoopBackOff with `database disk image is malformed` or similar in logs. SQLite WAL corruption is rare on a kernel that doesn't lose power, but it happens.

1. Drain the server so nothing is writing while you work:
   ```bash
   kubectl -n kuso scale deployment kuso-server --replicas=0
   ```
2. From a one-shot pod with the PVC mounted, try the SQLite recovery dance:
   ```bash
   kubectl -n kuso run sqlite-rescue --rm -it --restart=Never \
     --image=alpine \
     --overrides='{"spec":{"containers":[{"name":"sqlite-rescue","image":"alpine","stdin":true,"tty":true,"volumeMounts":[{"mountPath":"/data","name":"data"}]}],"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"kuso-server-go-data"}}]}}' \
     -- sh
   # inside the pod:
   apk add --no-cache sqlite
   cd /data
   sqlite3 kuso.db ".recover" | sqlite3 kuso.db.recovered
   mv kuso.db kuso.db.broken
   mv kuso.db.recovered kuso.db
   exit
   ```
3. Bring the server back:
   ```bash
   kubectl -n kuso scale deployment kuso-server --replicas=1
   kubectl -n kuso rollout status deployment/kuso-server
   ```

If `.recover` doesn't produce a usable DB, restore from the most recent snapshot (Path A). This is why the daily snapshot exists.

### Path C — the box is gone (disk failure, host loss)

You need a fresh `install.sh` run on a new box, then a `kuso restore` of the last good snapshot.

1. Provision a new host. Point your DNS at it (or update DNS first and wait for propagation).
2. Run `install.sh` exactly as you did the first time. The installer is idempotent on a fresh box; it'll build a clean cluster.
3. Before letting any user log in, restore the snapshot:
   ```bash
   kuso login --api https://kuso.example.com -u admin -p '<password from install summary>'
   kuso restore /backups/last-good.sqlite
   kubectl -n kuso rollout restart deployment/kuso-server
   ```
4. Wait for kuso to reconcile. The CRDs in your old cluster are gone; they need to be recreated. The fastest path is to re-import each project — `kuso` doesn't yet have a "rebuild CRs from SQLite" command, so this is partly manual:
   - Recreate each project (`kuso project create ...`).
   - Recreate each service (`kuso service create ...` or via the UI).
   - Trigger builds.
   - Restore addon data from your addon backups (see below).

This path is **slow** and reflects the single-box design. If you need warm-spare DR, kuso is the wrong tool.

## Addon data

`kuso addon backup` triggers an addon-specific backup job (Postgres → `pg_dump`, Redis → `BGSAVE` + RDB copy, etc.) and uploads to whatever object store the addon is configured against. See `kuso addon backup --help`. Snapshot the SQLite DB **and** the addons on the same schedule — restoring one without the other is a guaranteed bad time.

## Manual fallback (no CLI access)

If `kuso` itself is broken and you need to grab the DB by hand:

```bash
# On the host running k3s:
POD=$(kubectl -n kuso get pod -l app.kubernetes.io/name=kuso-server -o jsonpath='{.items[0].metadata.name}')
kubectl -n kuso exec "$POD" -- sqlite3 /var/lib/kuso/kuso.db \
  ".backup /tmp/kuso-snapshot.sqlite"
kubectl -n kuso cp "$POD:/tmp/kuso-snapshot.sqlite" ./kuso-snapshot.sqlite
```

`sqlite3 .backup` is online-safe and produces a consistent snapshot.

## What we don't do (and probably won't)

- **Multi-instance HA / replication.** SQLite is single-writer by design; running two `kuso-server` pods is not supported and will corrupt state. If you need this, you've outgrown kuso.
- **Continuous WAL streaming (Litestream-style).** Plausible future work; not built. Daily snapshots + 30-day retention is the recommended baseline.
- **Automatic restore.** Every restore is a destructive operation; we want the human in the loop.
