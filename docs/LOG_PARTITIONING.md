# Log table partitioning

`LogLine` (the pod-log search index) is the largest write-volume
table in the kuso control-plane DB. A 10 line/sec/pod workload on
100 pods writes ~8.6M rows/day; with the default 14-day retention
that's ~120M rows of working set. Routine retention prune (a chunked
`DELETE WHERE ts < ?`) keeps the table healthy, but each chunk still
holds short writer locks that contend with the logship inserter.

For installs that push past the default volume — a hundred chatty
services, a long-retention requirement, or both — declarative
partitioning by day moves prune from "delete 50k rows / repeat" to
"`DROP PARTITION`" — O(1), no lock contention.

## When to turn it on

- You're writing > 10M log rows / day.
- Daily cleanup is showing up as slow in `pg_stat_statements`.
- You want > 14 day retention without your `LogLine` table climbing
  past 50 GB.

If none of these apply, leave it off. The default chunked DELETE is
the right tool for small/medium installs and avoids the migration
risk.

## How to turn it on

1. Pick a maintenance window. The migration holds an exclusive lock
   on the legacy `LogLine` table for the rename phase (sub-second);
   the copy phase runs in 100k-row batches outside the lock but does
   write enough WAL to slow concurrent writers noticeably on a busy
   install. Plan ~15-30 min for a 50 GB table on a c2 cnpg cluster.

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
     retention window (default 14 days).

## Rollback

If something goes wrong during the migration (e.g. disk fills during
the copy phase), the legacy table is renamed to `LogLine_legacy` and
still holds every row. Restore by:

```sql
DROP TABLE "LogLine" CASCADE;          -- drops the partial partitioned
ALTER TABLE "LogLine_legacy" RENAME TO "LogLine";
```

…then unset `KUSO_LOG_PARTITIONING` and roll the deployment. The
chunked DELETE prune resumes immediately.

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
