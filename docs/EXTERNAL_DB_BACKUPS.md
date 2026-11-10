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
     namespace: kuso                    # or the project's spec.namespace
   spec:
     project: tickero
     kind: postgres
     external:
       secretName: tickero-planetscale   # the secret you created above
     backup:
       schedule: "0 3 * * *"             # 03:00 UTC daily
       retentionDays: 14                 # default 14; 0 = keep forever
   ```

3. **Make sure `kuso-backup-s3` is configured.** This is the cluster-wide Secret holding S3 credentials for the bucket where dumps land. Configure it in **Settings → Backups** in the UI, or `kubectl create secret generic kuso-backup-s3 -n kuso` with keys `bucket`, `endpoint`, `accessKeyId`, `secretAccessKey`, `region` (optional). `kuso backup settings set` writes the same Secret. To send one addon's dumps elsewhere, set `spec.backup.bucket` (or `kuso addon-backup schedule <project> <addon> --bucket <name>`); endpoint and credentials still come from `kuso-backup-s3`.

## What gets dumped

```
s3://<bucket>/<project>/<addon>/<YYYYMMDDTHHMMSSZ>.sql.gz
s3://<bucket>/<project>/<addon>/<YYYYMMDDTHHMMSSZ>.sql.gz.manifest.json
```

E.g. `s3://kuso-backups/tickero/tickero-db/20260527T030000Z.sql.gz`.

Each dump has a `.manifest.json` beside it with its sha256 and size. Dumps are encrypted by default: scheduling a backup makes the chart mint a per-addon key in the `<addon>-backup-key` Secret (kept on addon delete), and the `.sql.gz` is AES-256 encrypted with openssl before upload. Back that Secret up too — without it the dumps are unreadable. `kuso addon-backup restore` and `kuso addon-backup download --key` decrypt for you.

The dump is produced with `pg_dump --clean --if-exists --no-owner --no-acl`, using a `pg_dump` whose major version matches the server's (the run fails rather than guess) — `--no-owner --no-acl` is necessary for most managed providers because their dumps strip `GRANT`/`REVOKE` commands you don't have permission to recreate on restore. PlanetScale, Neon, and Supabase all document this same flag.

## Restoring

```bash
# Pull the dump locally (decrypted by the server):
kuso addon-backup list tickero tickero-db
kuso addon-backup download tickero tickero-db --key tickero/tickero-db/20260527T030000Z.sql.gz

# Or by hand, straight from S3 — decrypt with the addon's key first:
aws s3 cp s3://kuso-backups/tickero/tickero-db/20260527T030000Z.sql.gz . \
  --endpoint-url https://your-s3-endpoint
BACKUP_ENCRYPTION_KEY=$(kubectl -n <project-namespace> get secret tickero-db-backup-key -o jsonpath='{.data.key}' | base64 -d) \
  openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 -md sha256 -pass env:BACKUP_ENCRYPTION_KEY \
  -in 20260527T030000Z.sql.gz | gunzip -c | psql "$RESTORE_DATABASE_URL"
```

The dump carries `--clean --if-exists`, so it drops and recreates the objects it contains; objects that exist only in the target database are left alone. `kuso addon-backup list` / `kuso addon-backup restore` do the same from the CLI.

## Retention

Older dumps are pruned automatically every run: anything older than `retentionDays` is deleted from S3, except that the newest 3 dumps are always kept. Pruning runs **after** the new dump completes, so a failed dump never reduces your safety net, and a prune error never fails the Job.

## What this does NOT replace

- **The provider's own backups.** Use both. PlanetScale's branch-based point-in-time recovery is faster for "oops" recovery; your S3 dump is your "PlanetScale account was suspended" insurance.
- **Logical replication for cross-provider migration.** That's a one-shot ops task, not a recurring backup.

## Failure surface

The CronJob runs with `backoffLimit: 3` (up to three retries on failure). If every attempt fails you'll see:

- A failed Job in `kubectl get jobs -n <project>`.
- A `backup.failed` notification from the backup-health watcher (bell-icon feed + any configured webhooks).
- No new object in S3 for that timestamp.

Most common causes:
1. `DATABASE_URL` typo / expired creds → fix the secret, the next scheduled run picks it up.
2. `kuso-backup-s3` not configured → the run fails with `backup S3 not configured`; set it up, no need to re-create the addon.
3. Provider unreachable from the cluster → the Job waits 60s for `pg_isready`, then fails with `external postgres … unreachable`.
4. PlanetScale-specific: very-low-tier instances may hit pg_dump rate limits during peak hours. Move the schedule to off-hours.
