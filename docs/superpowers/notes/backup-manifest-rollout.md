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
