// Log partitioning — declarative range partitioning on LogLine by day.
//
// Why it's separate from schema.sql: existing installs already have a
// regular (unpartitioned) LogLine table with production data. PostgreSQL
// can't ALTER a regular table to partitioned in-place; the migration
// is rename → new partitioned → copy → swap, which holds a write lock
// for the duration of the copy. On a 200 GB LogLine that's hours.
//
// So we treat partitioning as an opt-in operator choice:
//
//   - schema.sql keeps the regular LogLine table for compatibility.
//     Fresh installs land on it and stay there until they opt in.
//   - Operators set KUSO_LOG_PARTITIONING=true on a maintenance
//     window. On first boot with the flag, the migration runs:
//     rename the old table, create the partitioned replacement, copy
//     data in batches, swap. Subsequent boots see the partitioned
//     state and skip the migration.
//   - Once partitioned, an EnsurePartitionForDay tick runs daily so
//     writes never hit "no partition for row".
//   - Prune switches from chunked DELETE to DROP PARTITION — O(1)
//     and lock-free vs concurrent writers.
//
// The reader side (SearchLogs, errorscan) is unchanged: a SELECT
// against a partitioned table transparently fans out across child
// partitions and returns a merged result.

package db

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// PartitionState reports whether LogLine is currently partitioned.
// Reads pg_partitioned_table — the canonical Postgres catalog for
// "is this table the parent of a partition hierarchy."
type PartitionState struct {
	Partitioned bool
	// PartitionCount counts child partitions when Partitioned=true,
	// zero otherwise. Useful for boot logging so operators see
	// "20 daily partitions" at a glance.
	PartitionCount int
}

// LogPartitionState returns the current partition state for LogLine.
// Cheap (two small catalog reads); safe to call on every boot.
func (d *DB) LogPartitionState(ctx context.Context) (PartitionState, error) {
	var partitioned bool
	if err := d.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM pg_partitioned_table pt
			JOIN pg_class c ON c.oid = pt.partrelid
			WHERE c.relname = 'LogLine'
		)
	`).Scan(&partitioned); err != nil {
		return PartitionState{}, fmt.Errorf("query partition state: %w", err)
	}
	if !partitioned {
		return PartitionState{}, nil
	}
	var count int
	if err := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_inherits i
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname = 'LogLine'
	`).Scan(&count); err != nil {
		return PartitionState{}, fmt.Errorf("count partitions: %w", err)
	}
	return PartitionState{Partitioned: true, PartitionCount: count}, nil
}

// EnsureLogPartitionForDay creates the daily partition covering the
// given timestamp (UTC). The partition name follows the convention
// `LogLine_YYYY_MM_DD` so operators can find/drop them by date with
// a glance at `\d+ "LogLine"`. Idempotent: re-running with the same
// day is a no-op (`IF NOT EXISTS`).
//
// Called from the daily cleanup tick AND immediately before writes
// near a day boundary (the logship flusher could otherwise insert a
// 00:00:05 row into a not-yet-created partition and 23-error).
//
// Returns nil and no-ops when LogLine isn't partitioned — callers
// can run this unconditionally; the function gates itself on state.
func (d *DB) EnsureLogPartitionForDay(ctx context.Context, day time.Time) error {
	st, err := d.LogPartitionState(ctx)
	if err != nil {
		return err
	}
	if !st.Partitioned {
		return nil
	}
	// Normalise to UTC midnight; partition bounds are [start, end).
	start := time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	partName := fmt.Sprintf("LogLine_%04d_%02d_%02d", start.Year(), int(start.Month()), start.Day())
	stmt := fmt.Sprintf(`
		CREATE TABLE IF NOT EXISTS %q
		PARTITION OF "LogLine"
		FOR VALUES FROM ('%s') TO ('%s')
	`, partName, start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"))
	if _, err := d.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("create partition %s: %w", partName, err)
	}
	return nil
}

// EnsureLogPartitionWindow keeps the next `days` daily partitions
// provisioned. Called from the daily cleanup tick so that even a
// week-long server outage doesn't leave the flusher hitting "no
// partition for row" for ~7 days after recovery. days=3 is the
// default (yesterday + today + tomorrow) — enough cushion for
// timezone surprises + clock drift.
func (d *DB) EnsureLogPartitionWindow(ctx context.Context, anchor time.Time, days int) error {
	if days <= 0 {
		days = 3
	}
	for i := -1; i < days; i++ {
		if err := d.EnsureLogPartitionForDay(ctx, anchor.AddDate(0, 0, i)); err != nil {
			return err
		}
	}
	return nil
}

// PruneLogPartitionsBefore drops daily partitions whose end-of-day is
// strictly before `before`. Returns the number of partitions dropped.
// Each DROP is its own transaction so a partial outage doesn't undo
// the work already done; the catalog locks are millisecond-scale.
//
// When LogLine isn't partitioned, returns (0, nil) — the caller's
// chunked DELETE fallback handles that case. Callers can wire both
// PruneLogsOlderThan and this method side-by-side and let the
// partition state decide which one does work.
func (d *DB) PruneLogPartitionsBefore(ctx context.Context, before time.Time) (int, error) {
	st, err := d.LogPartitionState(ctx)
	if err != nil {
		return 0, err
	}
	if !st.Partitioned {
		return 0, nil
	}
	// List partition names + their upper bound. Partition bounds are
	// stored as text expressions on pg_class.relpartbound; parsing
	// them ourselves is fragile. Use the convention-based name:
	// LogLine_YYYY_MM_DD → covers [start, end) for that day.
	rows, err := d.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_inherits i
		JOIN pg_class p ON p.oid = i.inhparent
		JOIN pg_class c ON c.oid = i.inhrelid
		WHERE p.relname = 'LogLine'
		ORDER BY c.relname ASC
	`)
	if err != nil {
		return 0, fmt.Errorf("list partitions: %w", err)
	}
	defer rows.Close()
	var dropped int
	cutoff := before.UTC()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return dropped, fmt.Errorf("scan partition name: %w", err)
		}
		end, ok := dayPartitionEnd(name)
		if !ok {
			// Skip partitions whose name doesn't match the convention —
			// an operator may have stamped a manual one (legacy data
			// fold-in). We refuse to drop what we don't fully recognise.
			continue
		}
		if !end.Before(cutoff) {
			// This partition's tail is at or after the cutoff — keep.
			// Partitions are listed in name (date) order, so we could
			// `break` here, but staying defensive: if an old straggler
			// somehow sorts after a newer one, we still get to it.
			continue
		}
		if _, err := d.ExecContext(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %q`, name)); err != nil {
			return dropped, fmt.Errorf("drop partition %s: %w", name, err)
		}
		dropped++
	}
	return dropped, rows.Err()
}

// dayPartitionEnd parses a partition name of the convention shape
// `LogLine_YYYY_MM_DD` and returns end-of-day in UTC. Returns ok=false
// when the name doesn't match the convention — caller skips it.
func dayPartitionEnd(name string) (time.Time, bool) {
	const prefix = "LogLine_"
	if !strings.HasPrefix(name, prefix) {
		return time.Time{}, false
	}
	rest := name[len(prefix):] // "YYYY_MM_DD"
	if len(rest) != len("2006_01_02") {
		return time.Time{}, false
	}
	t, err := time.Parse("2006_01_02", rest)
	if err != nil {
		return time.Time{}, false
	}
	// End-of-day = midnight on the next day (partition upper bound is
	// exclusive, so a partition for 2026-05-18 ends at 2026-05-19 00:00).
	return t.Add(24 * time.Hour), true
}

// MigrateLogLineToPartitioned converts a regular LogLine table into a
// partitioned table. Called only when KUSO_LOG_PARTITIONING=true AND
// the current state is unpartitioned. NOT idempotent across replicas —
// the caller must hold the singleton lease (leader-elected wiring) so
// only one replica attempts the migration.
//
// The migration shape:
//
//  1. Rename "LogLine" → "LogLine_legacy".
//  2. Create the new partitioned "LogLine" with PRIMARY KEY (id, ts).
//  3. Provision partitions covering the legacy data + a future window.
//  4. Copy legacy rows into the new table in 100k-row batches.
//  5. Drop the legacy table.
//
// Holds an EXCLUSIVE lock on the legacy table for the rename + drop —
// brief (<1s). The copy phase runs in batches outside any explicit
// lock; logship will see a moment of "table doesn't exist" between
// step 1 and step 2 and retry on its next flush.
//
// Returns nil + no-ops when already partitioned. Logs progress at
// info level so operators see the copy advance during the maintenance
// window.
func (d *DB) MigrateLogLineToPartitioned(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	st, err := d.LogPartitionState(ctx)
	if err != nil {
		return err
	}
	if st.Partitioned {
		logger.Info("log-partition: already partitioned, skipping migration")
		return nil
	}
	logger.Info("log-partition: starting migration to partitioned LogLine")
	// 1. Rename in its own txn so we own the lock briefly.
	if _, err := d.ExecContext(ctx, `ALTER TABLE "LogLine" RENAME TO "LogLine_legacy"`); err != nil {
		return fmt.Errorf("rename legacy: %w", err)
	}
	logger.Info("log-partition: renamed LogLine → LogLine_legacy")
	// Postgres carries the table's indexes along with the RENAME, keeping
	// their ORIGINAL names — so "LogLine_project_service_ts_idx" et al.
	// now sit on LogLine_legacy. Index names are schema-global, so the
	// CREATE INDEX calls below (same names, new partitioned table) would
	// collide with "relation already exists". Drop them now: the legacy
	// table is read only via SELECT MIN/MAX + a PK-ordered batched copy
	// (the PK index survives), then dropped entirely at the end, so its
	// secondary indexes are dead weight from this point on.
	for _, idx := range []string{
		"LogLine_project_service_ts_idx",
		"LogLine_ts_idx",
		"LogLine_line_trgm_idx",
	} {
		if _, err := d.ExecContext(ctx, `DROP INDEX IF EXISTS "`+idx+`"`); err != nil {
			return fmt.Errorf("drop legacy index %s: %w", idx, err)
		}
	}
	// 2. Create the new partitioned table. The PK must include the
	// partition key (ts) — Postgres rule for partitioned tables.
	if _, err := d.ExecContext(ctx, `
		CREATE TABLE "LogLine" (
			"id" BIGSERIAL,
			"ts" TIMESTAMPTZ NOT NULL,
			"pod" TEXT NOT NULL,
			"project" TEXT NOT NULL DEFAULT '',
			"service" TEXT NOT NULL DEFAULT '',
			"env" TEXT NOT NULL DEFAULT '',
			"line" TEXT NOT NULL DEFAULT '',
			PRIMARY KEY ("id", "ts")
		) PARTITION BY RANGE ("ts")
	`); err != nil {
		return fmt.Errorf("create partitioned: %w", err)
	}
	// Recreate the indexes — they propagate to every partition the
	// table parents. The trigram index needs pg_trgm; we don't gate
	// on its presence because the schema.sql apply path will have
	// already attempted CREATE EXTENSION before this code runs.
	if _, err := d.ExecContext(ctx, `CREATE INDEX "LogLine_project_service_ts_idx" ON "LogLine"("project","service","ts" DESC)`); err != nil {
		return fmt.Errorf("create idx project_service_ts: %w", err)
	}
	if _, err := d.ExecContext(ctx, `CREATE INDEX "LogLine_ts_idx" ON "LogLine"("ts")`); err != nil {
		return fmt.Errorf("create idx ts: %w", err)
	}
	if _, err := d.ExecContext(ctx, `CREATE INDEX "LogLine_line_trgm_idx" ON "LogLine" USING GIN ("line" gin_trgm_ops)`); err != nil {
		// pg_trgm missing → the schema apply also skipped it. Not
		// fatal — alert performance is degraded but logs work.
		logger.Warn("log-partition: trigram index skipped", "err", err)
	}
	// 3. Provision partitions for the legacy data span + the future
	// window. Query MIN(ts) / MAX(ts) on the legacy table to bound it.
	var minTs, maxTs *time.Time
	if err := d.QueryRowContext(ctx, `SELECT MIN("ts"), MAX("ts") FROM "LogLine_legacy"`).Scan(&minTs, &maxTs); err != nil {
		return fmt.Errorf("scan legacy bounds: %w", err)
	}
	if minTs == nil || maxTs == nil {
		// Legacy table was empty — just provision the standard window.
		if err := d.EnsureLogPartitionWindow(ctx, time.Now(), 3); err != nil {
			return err
		}
	} else {
		day := minTs.UTC().Truncate(24 * time.Hour)
		end := maxTs.UTC().Add(24 * time.Hour).Truncate(24 * time.Hour)
		for day.Before(end) {
			if err := d.EnsureLogPartitionForDay(ctx, day); err != nil {
				return err
			}
			day = day.Add(24 * time.Hour)
		}
		// Also future window so first-write-after-migration doesn't
		// race the daily tick.
		if err := d.EnsureLogPartitionWindow(ctx, time.Now(), 3); err != nil {
			return err
		}
	}
	// 4. Copy data in batches. id is preserved so error-scan
	// watermarks keep working across the cutover.
	//
	// Paginate by the actual last-id seen (WHERE id > lastID), NOT by a
	// running row count. After time-based pruning the legacy table has
	// id GAPS — using the copied-row count as the `id >` watermark would
	// terminate the loop the moment the count fell short of the next id
	// (e.g. 50 rows copied, next ids start at 1000: `id > 50` still
	// matches, but `id > <count>` logic drifts and skips/duplicates rows
	// as gaps accumulate). Tracking the max id actually copied is
	// gap-safe: each batch strictly advances past the highest id written.
	const batch = 100_000
	var (
		copiedTotal int64
		lastID      int64
	)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		// RETURNING the copied ids lets us both count the batch and pick
		// up its max id as the next watermark in a single round-trip.
		rows, err := d.QueryContext(ctx, `
			WITH moved AS (
				INSERT INTO "LogLine" ("id", "ts", "pod", "project", "service", "env", "line")
				SELECT "id", "ts", "pod", "project", "service", "env", "line"
				FROM "LogLine_legacy"
				WHERE "id" > $1
				ORDER BY "id" ASC
				LIMIT $2
				RETURNING "id"
			)
			SELECT COUNT(*), COALESCE(MAX("id"), 0) FROM moved
		`, lastID, batch)
		if err != nil {
			return fmt.Errorf("copy batch: %w", err)
		}
		var (
			n        int64
			batchMax int64
			scanErr  error
			haveRow  bool
		)
		if rows.Next() {
			haveRow = true
			scanErr = rows.Scan(&n, &batchMax)
		}
		rows.Close()
		if scanErr != nil {
			return fmt.Errorf("scan copy batch: %w", scanErr)
		}
		if !haveRow || n == 0 {
			break
		}
		copiedTotal += n
		lastID = batchMax
		logger.Info("log-partition: copy progress", "rows", copiedTotal, "lastId", lastID)
	}
	// 5. Drop the legacy table. The new partitioned table now carries
	// every row that was in it, plus any new writes that landed
	// between step 2 and now (those went to the new table directly).
	if _, err := d.ExecContext(ctx, `DROP TABLE "LogLine_legacy"`); err != nil {
		return fmt.Errorf("drop legacy: %w", err)
	}
	// Reseed the BIGSERIAL so future inserts continue past the
	// max(id) we copied — without this, the SERIAL nextval is back
	// at 1 and the first new insert collides with a copied id.
	if _, err := d.ExecContext(ctx, `
		SELECT setval(pg_get_serial_sequence('"LogLine"', 'id'),
		              COALESCE((SELECT MAX("id") FROM "LogLine"), 0) + 1,
		              false)
	`); err != nil {
		return fmt.Errorf("reseed serial: %w", err)
	}
	logger.Info("log-partition: migration complete", "totalRows", copiedTotal)
	return nil
}
