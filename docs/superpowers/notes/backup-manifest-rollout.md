# Backup manifest rollout

Piece 2 of the Openship-inspired improvements. Two sides ship separately:

- **Backup side (helm chart):** the `kusoaddon` backup CronJob now writes a
  `<key>.manifest.json` beside each artifact (sha256 + size + kind). This only
  takes effect after an addon's helm release is re-rendered to the new chart —
  trigger an addon update (or wait for the operator's next reconcile) so the
  CronJob picks up the change. Existing addons keep using their old CronJob
  until then.
- **Restore side (server-go binary):** the restore Job now downloads the
  manifest, verifies the artifact's sha256, and aborts before touching the DB on
  mismatch. This ships in the server-go binary, so `make ship` (then the
  updater tick) is what activates restore verification.

**Backward compatibility:** restore is safe against pre-manifest backups — a
missing manifest logs `integrity NOT verified, proceeding` and applies the dump
as before. No operator action required for old backups.

**Notes / follow-ups:**
- The `kuso-backup` image (`build/backup/Dockerfile`, alpine:3.21) provides
  `sha256sum`/`wc` via BusyBox — no image change was needed for hashing.
- Pre-existing gap noticed during this work: that image installs no
  `redis-cli`, so the redis backup CronJob would already fail today. Out of
  scope for Piece 2; worth fixing when the producer registry (Piece 3) lands.
- The manifest JSON is emitted with `printf`, not a heredoc: the CronJob script
  runs inside a YAML block scalar where an indented heredoc terminator is never
  matched and would silently swallow the rest of the script.

## Piece 3 addendum — producer registry + mongodb

- New `server-go/internal/backup` registry drives the restore Job's script
  per addon kind (postgres/redis/mongodb registered; others → "not
  restorable" 400).
- mongodb now has a scheduled backup CronJob branch (mongodump --archive
  --gzip) + sha256 manifest, and restore via mongorestore --drop.
- The `kuso-backup` image gained `mongodb-tools` (mongodump/mongorestore)
  AND `redis` (redis-cli — the redis branch was already broken without it).
  This requires `make backup-image` to rebuild+push before mongodb/redis
  backups run on the cluster — the auto-updater does NOT rebuild this image.
  apk names verified installable in alpine:3.21.
- Other addon kinds (valkey/clickhouse/rabbitmq/meilisearch/nats/redpanda)
  are registry-ready but not yet implemented — follow-up work.

## Piece 4 addendum — pre-deploy snapshot

- New `spec.snapshotBeforeDeploy` (bool) on KusoService, mirrored to
  KusoEnvironment. Opt-in; postgres-only; fires only when a release hook
  also exists. Snapshot runs before the release/migration Job; snapshot
  infra-failure blocks the deploy; on migration failure the build is
  annotated `kuso.sislelabs.com/predeploy-snapshot-keys` with the snapshot
  S3 keys for a one-click restore-to-pre-deploy.
- CRD schema changed (both kusoservices + kusoenvironments) → needs
  `kubectl apply` of the two CRD YAMLs on the cluster (auto-updater only
  flips image tags). CRD golden test refreshed (`KUSO_UPDATE_GOLDENS=1`).
- Snapshot Jobs reuse the pg_dump + sha256 manifest flow (Pieces 2–3), so
  they appear in the existing backup list and restore through the verified
  path. Manifest carries extra `trigger`/`buildRef` fields (ignored by the
  Piece-2 parser — forward-compatible).
- **Surface gap (follow-up):** the field is wired end-to-end server-side and
  settable via the API (incl. `kuso api PATCH .../services/<svc> -f
  snapshotBeforeDeploy=true`). A dedicated CLI flag + a web toggle/restore
  button are NOT yet added — release isn't surfaced in the CLI kuso.yaml
  either, so this matches the existing pattern. Add web toggle + a
  "restore pre-deploy snapshot" button (reads the build annotation, calls
  the existing restore endpoint) as a focused follow-up.

## Piece 5 addendum — mysql addon kind + producer

- MySQL is now a first-class addon kind: `operator/helm-charts/kusoaddon/
  templates/mysql.yaml` (mysql:8 StatefulSet + Service + conn Secret with
  MYSQL_* + MYSQL_URL/DATABASE_URL, resource-policy=keep, annotation-free
  VCT, password-reuse + drift guard). Added to `$supported` + `$noHA`
  (unsupported.yaml) and `noHAKinds` (addons.go). No HA in v1.
- Backup: mysqldump CronJob branch + a `mysqlProducer` in the registry
  (mysqldump / `mysql` restore, sha256 manifest). Restore conn env branch
  added for kind=mysql.
- Backup image gained `mysql-client` (alpine mariadb-client, wire-compatible;
  verified installable in alpine:3.21). Needs `make backup-image` to
  rebuild+push before mysql backups run on the cluster.
- Web: mysql added to the AddAddonDialog kind picker (AddonIcon already had
  a mysql brand + mariadb alias). Web typecheck passes. CLI accepts
  `--kind mysql` with no client-side allowlist change (kind is validated
  server/chart-side).
- CRD/chart apply on cluster required (new template + validation lists);
  the auto-updater only flips image tags.
- Deferred (documented): mysql HA, DB browser (`kuso db sql`) for mysql,
  previewdb clone for mysql. mongodb was already a kind (Piece 3), so this
  piece was mysql-only.
