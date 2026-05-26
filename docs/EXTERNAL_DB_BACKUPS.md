# Backing up an external Postgres into kuso's S3

Managed Postgres (PlanetScale, Neon, Supabase, Hetzner Cloud, RDS, etc.) gives you HA + point-in-time recovery from the provider. That's not the same thing as **your own** snapshot in **your own** bucket. If the provider has an outage, an account-level mishap, or you want to dev-restore prod data locally, you want your own dump.

Kuso's addon backup CronJob handles this for any `kind=postgres` addon whose `spec.external.secretName` is set.

## Setup

1. **Create a Secret** with `DATABASE_URL` pointing at your managed instance:

   ```bash
   kubectl -n <project-namespace> create secret generic tickero-planetscale \
     --from-literal=DATABASE_URL='postgres://user:pass@aws.connect.psdb.cloud/db?sslmode=require'
   ```

2. **Author the addon** (via `kuso.yaml`, the UI, or `kubectl apply`):

   ```yaml
   apiVersion: application.kuso.sislelabs.com/v1alpha1
   kind: KusoAddon
   metadata:
     name: tickero-db
     namespace: tickero
   spec:
     project: tickero
     kind: postgres
     external:
       secretName: tickero-planetscale   # the secret you created above
     backup:
       schedule: "0 3 * * *"             # 03:00 UTC daily
       retentionDays: 14
   ```

3. **Make sure `kuso-backup-s3` is configured.** This is the cluster-wide Secret holding S3 credentials for the bucket where dumps land. Configure it in **Settings → Backups** in the UI, or `kubectl create secret generic kuso-backup-s3 -n kuso` with keys `bucket`, `endpoint`, `accessKeyId`, `secretAccessKey`, `region` (optional).

## What gets dumped

```
s3://${KUSO_BACKUP_BUCKET}/<project>/<addon>/<utc-iso-timestamp>.sql.gz
```

E.g. `s3://kuso-backups/tickero/tickero-db/20260527T030000Z.sql.gz`.

The dump is produced with `pg_dump --no-owner --no-acl` — necessary for most managed providers because their dumps strip `GRANT`/`REVOKE` commands you don't have permission to recreate on restore. PlanetScale, Neon, and Supabase all document this same flag.

## Restoring

```bash
# Pull the dump locally:
aws s3 cp s3://kuso-backups/tickero/tickero-db/20260527T030000Z.sql.gz . \
  --endpoint-url https://your-s3-endpoint

# Restore against an empty database:
gunzip -c 20260527T030000Z.sql.gz | psql "$RESTORE_DATABASE_URL"
```

Restoring into a non-empty database needs `--clean --if-exists` baked into the dump or a `DROP SCHEMA public CASCADE; CREATE SCHEMA public;` first — out of scope here.

## Retention

Older dumps are pruned automatically every run: anything older than `retentionDays` is deleted from S3. Pruning runs **after** the new dump completes, so a failed dump never reduces your safety net.

## What this does NOT replace

- **The provider's own backups.** Use both. PlanetScale's branch-based point-in-time recovery is faster for "oops" recovery; your S3 dump is your "PlanetScale account was suspended" insurance.
- **Logical replication for cross-provider migration.** That's a one-shot ops task, not a recurring backup.

## Failure surface

The CronJob runs with `backoffLimit: 1` (one retry on container failure). If both attempts fail you'll see:

- A failed Job in `kubectl get jobs -n <project>`.
- An entry in the bell-icon feed (Settings → Notifications surfaces it via the standard Job-failed channel).
- No new object in S3 for that timestamp.

Most common causes:
1. `DATABASE_URL` typo / expired creds → fix the secret, the next scheduled run picks it up.
2. `kuso-backup-s3` not configured → set it up, no need to re-create the addon.
3. PlanetScale-specific: very-low-tier instances may hit pg_dump rate limits during peak hours. Move the schedule to off-hours.
