# Log table partitioning

`LogLine` (the pod-log search index) is the largest write-volume
table in the kuso control-plane DB. A 10 line/sec/pod workload on
100 pods writes ~8.6M rows/day; with the default 7-day retention
(`KUSO_LOG_RETENTION_DAYS`) that's ~60M rows of working set. Routine retention prune (a chunked
`DELETE WHERE ts < ?`) keeps the table healthy, but each chunk still
holds short writer locks that contend with the logship inserter.

For installs that push past the default volume — a hundred chatty
services, a long-retention requirement, or both — declarative
partitioning by day moves prune from "delete 50k rows / repeat" to
"`DROP PARTITION`" — O(1), no lock contention.

## Size budget

Retention is by time, so it doesn't bound size. The control-plane
Postgres volume is 10Gi by default, and at ~630 bytes per stored line
(heap plus indexes) a week at ~28 lines/s cluster-wide fills it. The
log shipper also keeps the table under a byte budget:

- `KUSO_LOG_MAX_BYTES` (bytes or a quantity such as `4Gi`; `0` turns
  it off). The default is 40% of the `kuso-postgres` volume, read from
  the CNPG Cluster (`KUSO_DB_VOLUME_BYTES` overrides it), or 4Gi when
  the size can't be read.
- Every 5 minutes it estimates the bytes held by live rows. Over
  budget, it deletes the oldest lines down to 90% of the budget, at
  most 200k rows and 60s per run, in 5k-row transactions on the direct
  connection. A cluster that upgrades already over budget catches up
  over several runs.
- A DELETE frees space for reuse but doesn't shrink the files. When the
  database passes 80% of the volume it sends a `platform.unhealthy`
  notification for `control-plane-db`. Above 90%, if the log table has
  less than 10% reusable space, it stops storing logs until usage drops,
  so auth and the API keep working. To get the space back, run
  `VACUUM FULL "LogLine"` in a maintenance window (it locks the table)
  or switch to partitioning below, which frees space when a day is
  dropped.

## When to turn it on

- You're writing > 10M log rows / day.
- Daily cleanup is showing up as slow in `pg_stat_statements`.
- You want > 7 day retention without your `LogLine` table climbing
  past 50 GB.

If none of these apply, leave it off. The default chunked DELETE is
the right tool for small/medium installs and avoids the migration
risk.

## How to turn it on

1. Pick a maintenance window. The migration holds an exclusive lock
   on the legacy `LogLine` table for the rename phase (sub-second);
   the copy phase runs in 100k-row batches outside the lock but does
   write enough WAL to slow concurrent writers noticeably on a busy
   install. Plan ~15-30 min for a 50 GB table on a c2 cnpg cluster;
   the migration runs under a hard 30-minute timeout, so a bigger
   table aborts mid-copy (see Rollback).

2. Set the env on the kuso-server Deployment:

   ```bash
   kubectl -n kuso set env deployment/kuso-server \
     KUSO_LOG_PARTITIONING=true
   ```

3. The next leader replica picks up the flag and runs
   `MigrateLogLineToPartitioned` once. Tail the logs:

   ```bash
   kubectl -n kuso logs -l app.kubernetes.io/name=kuso-server -f \
     | grep log-partition
   ```

   You should see, in order:
   - `log-partition: starting migration to partitioned LogLine`
   - `log-partition: renamed LogLine → LogLine_legacy`
   - one or more `log-partition: copy progress rows=…` lines
   - `log-partition: migration complete totalRows=N`

4. After the migration completes the daily cleanup tick takes over.
   It runs at boot and every 24h thereafter:
   - Provisions the next 3 daily partitions so writes never hit
     "no partition for row" at the midnight boundary.
   - Drops every partition whose end-of-day is past the configured
     retention window (`KUSO_LOG_RETENTION_DAYS`, default 7 days).

## Rollback

If something goes wrong during the migration (e.g. disk fills during
the copy phase, or the 30-minute timeout hits), the legacy table is
renamed to `LogLine_legacy` and still holds every row (it is only
dropped once the copy finishes). Restore by:

```sql
DROP TABLE "LogLine" CASCADE;          -- drops the partial partitioned
ALTER TABLE "LogLine_legacy" RENAME TO "LogLine";
```

…then unset `KUSO_LOG_PARTITIONING` and roll the deployment. The
chunked DELETE prune resumes immediately. The migration dropped the
legacy table's secondary indexes; the restart rebuilds them
(`schema.sql` plus the deferred-index build), which takes a while on
a large table.

## What's preserved

- `id` column stays a globally-unique BIGSERIAL across partitions.
- The error-scan watermark (`WHERE id > ?`) keeps working — partition
  boundaries don't affect id ordering for sequential reads.
- `pg_trgm` GIN index over `line` propagates to every child partition,
  so alert engine `ILIKE '%query%'` performance is unchanged.
- All current readers (search, alerts, error-scan) work against the
  partitioned parent with no code changes.

## What changes for queries

Nothing for application code. The Postgres planner fans out
`SELECT ... FROM "LogLine" WHERE …` across child partitions
transparently. The `(ts >= ? AND ts < ?)` filter in `SearchLogs`
becomes a partition-pruning win: only partitions whose range
overlaps the time window are scanned.
