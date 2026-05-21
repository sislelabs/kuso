# Postgres Point-in-Time Recovery (PITR) — Design

**Date:** 2026-05-21
**Status:** Draft — for review before implementation

## Problem

Single-node Postgres addons today get **scheduled `pg_dump` snapshots**
to S3 (the `backup-cronjob.yaml` chart). That is *snapshot* recovery:
you can restore to 03:00 last night, but not to 14:32:07 — the moment
just before someone ran `DELETE FROM orders` without a `WHERE`. The
window of unrecoverable data loss is the whole gap since the last
snapshot (up to 24h on a daily schedule).

HA (CNPG) addons can do real PITR via CNPG's barman WAL archiving, but
it is documented as out-of-band (`spec.backup.barmanObjectStore` set
by `kubectl edit`) — not surfaced in kuso.

This adds **first-class PITR for Postgres addons**: continuous WAL
archiving + base backups, and a "restore to timestamp T" operation in
the UI/CLI. Railway shipped native PITR in May 2026 and treats it as a
headline; this closes that gap.

## Goals

- Opt-in per-addon: `KusoAddon.spec.backup.pitr.enabled`.
- **Single-node Postgres:** continuous WAL archiving to S3 via
  `archive_command` + periodic base backups. Restore to any timestamp
  within the retention window.
- **HA (CNPG) Postgres:** wire CNPG's native barman PITR through the
  kuso chart + API (CNPG already does the hard part — kuso just needs
  to render `spec.backup.barmanObjectStore` and a recovery bootstrap).
- A UI "Restore to point in time" action: pick a timestamp, restore
  into the source addon (destructive, confirmed) or a new addon
  (rehearsal — the existing `--into` pattern).

## Non-goals

- PITR for non-Postgres addons. Postgres only — WAL archiving is a
  Postgres concept.
- Cross-region / cross-cluster DR. WAL ships to the same S3 the
  snapshot backups already use.
- Sub-second RPO guarantees. `archive_command` ships completed WAL
  segments; the realistic RPO is "one WAL segment" (default 16 MB or
  the `archive_timeout`, whichever comes first — we set
  `archive_timeout = 60s` so an idle DB still ships a segment/min).

## Approach

Two engines, one API — mirroring how the pooler feature already
branches single-node vs CNPG.

### Single-node Postgres — WAL archiving + base backups

The single-node `postgres.yaml` chart, when `backup.pitr.enabled`:

- Configures the Postgres container for archiving:
  `wal_level = replica`, `archive_mode = on`,
  `archive_command = 'wal-g wal-push %p'` (or a plain `s3cmd put`
  wrapper — see "WAL shipping tool" below), `archive_timeout = 60s`.
- Mounts the `kuso-backup-s3` credentials (already used by the
  snapshot CronJob) so WAL segments ship to
  `s3://<bucket>/<project>/<addon>/wal/`.
- A **base-backup CronJob** (separate from, and replacing, the
  `pg_dump` cronjob when PITR is on) runs `pg_basebackup` /
  `wal-g backup-push` on the configured schedule. PITR = latest base
  backup ≤ T, then replay WAL up to T.
- Retention: WAL + base backups older than
  `backup.retentionDays` are pruned (a prune step, as the snapshot
  path already has).

**WAL shipping tool:** use **WAL-G** (single static binary, S3-native,
handles base backup + WAL push + retention + the restore replay). It
runs as a sidecar/initContainer-free `archive_command` callout. The
alternative — hand-rolled `s3cmd` for WAL + `pg_basebackup` — works
but we'd reimplement retention and restore-replay ourselves. WAL-G is
the right call; it is the de-facto tool and the kuso-backup image can
bundle it.

### HA (CNPG) Postgres — native barman

The `postgres-ha.yaml` chart, when `backup.pitr.enabled`, renders
`spec.backup.barmanObjectStore` on the CNPG `Cluster` (S3 endpoint +
the `kuso-backup-s3` credentials) and a `ScheduledBackup` CR. CNPG
then does continuous WAL archiving + scheduled base backups itself.
Restore is a CNPG `Cluster` with `bootstrap.recovery` pointing at the
object store + a `recoveryTarget.targetTime`. kuso renders that into a
*new* addon for the rehearsal path; in-place restore recreates the
Cluster with the recovery bootstrap.

### API + restore operation

- `KusoAddon.spec.backup.pitr.enabled` (bool) — the toggle.
- New endpoint `POST /api/projects/{p}/addons/{a}/restore-pitr`
  body `{ targetTime: <RFC3339>, into?: <new-addon-name> }`.
  - `into` set → provision a new addon restored to `targetTime`
    (rehearsal; non-destructive — the existing `--into` semantics).
  - `into` unset → in-place: destructive, requires a typed
    confirmation, gated on `addons:write`.
- The restore is a Job (single-node: WAL-G `backup-fetch` + a
  `recovery.conf`/`postgresql.auto.conf` with `recovery_target_time`;
  HA: a CNPG recovery `Cluster`). Progress + result surface as a
  `NotificationEvent` and audit row.
- `GET /api/projects/{p}/addons/{a}/pitr-window` returns the
  earliest and latest restorable timestamps (earliest = oldest base
  backup, latest = now) so the UI can bound the timestamp picker.

### UI

- Addon → Backups tab: when `pitr.enabled`, a "Restore to point in
  time" panel — a datetime picker bounded by the `pitr-window`, an
  "Into" choice (overwrite this addon / new addon name), and a
  confirm dialog. In-place restore is destructive → typed-name
  confirm, like the addon-delete flow.
- Addon → Settings: the `pitr.enabled` toggle, with a one-line cost
  note (WAL archiving adds steady S3 writes + storage).

### CLI

`kuso addon-backup` gains `kuso addon-backup pitr <project> <addon>
--to <RFC3339> [--into <name>]`, mirroring the existing `restore`
subcommand.

## Blast radius / risks

- **Data-safety feature — correctness is paramount.** A PITR that
  silently loses WAL, or restores to the wrong timestamp, is worse
  than no PITR. The restore Job must verify the recovery target was
  reached (`pg_last_wal_replay_lsn` / CNPG status) and fail loudly
  otherwise. The rehearsal (`--into`) path must be the documented
  default — never in-place without an explicit, confirmed choice.
- **S3 dependency** — PITR is only as durable as the `kuso-backup-s3`
  bucket. If that secret is missing, enabling PITR must fail at the
  API with a clear message, not silently archive nowhere.
- **`archive_mode` needs a restart** — turning PITR on flips
  `archive_mode`, which is not reloadable; the single-node addon pod
  restarts once on enable. Surfaced via the blast-radius dialog.
- **Disk** — `archive_mode = on` means Postgres retains WAL until
  `archive_command` succeeds. If S3 is unreachable for a long time,
  pg_wal grows and can fill the PVC. The chart sets a conservative
  `archive_timeout` and the health watcher should alert on a stuck
  archiver (a follow-up `archive.failing` event).
- **Replaces, not adds to, the pg_dump cronjob** — when PITR is on,
  the chart suppresses the old `pg_dump` snapshot CronJob (base
  backups subsume it). Documented so operators don't expect both.

## Decisions to confirm

1. **WAL-G as the shipping tool** — yes; don't hand-roll WAL push +
   retention + replay.
2. **Rehearsal (`--into`) is the default restore mode** — yes;
   in-place restore requires an explicit destructive confirm.
3. **PITR replaces the pg_dump snapshot CronJob when enabled** — yes;
   running both wastes S3 and confuses retention.
4. **Retention reuses `backup.retentionDays`** — yes; one knob for
   both WAL+base and the old snapshots.
5. **`archive_timeout = 60s`** — bounds RPO for idle databases at one
   segment/minute. Reasonable default; not exposed in v1.

## Rollout

- CRD change (`KusoAddon.spec.backup.pitr`) → `kubectl apply` the
  kusoaddons CRD.
- `kuso-backup` image bundles WAL-G → image rebuild.
- New endpoints + restore Job template → server + chart changes.
- Ship via `make ship`.
