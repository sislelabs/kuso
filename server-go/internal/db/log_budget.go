package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LogStorageStats is the input to the LogLine byte budget (SCL-1). Every
// figure covers the whole partition tree, so it is right whether or not
// LogLine is partitioned.
type LogStorageStats struct {
	// TotalBytes is what LogLine occupies on disk: heap, TOAST and indexes.
	TotalBytes int64
	HeapBytes  int64
	IndexBytes int64
	// LiveRows is the stats collector's live-tuple count. It drops as
	// soon as a DELETE commits, unlike the on-disk size, which a plain
	// DELETE + autovacuum never shrinks.
	LiveRows int64
	// AvgRowBytes is the mean width of a sample of the newest rows.
	AvgRowBytes float64
	// DatabaseBytes is pg_database_size of the whole control-plane DB.
	DatabaseBytes int64
}

// BytesPerRow estimates what one live row costs including its share of
// the indexes. Heap and indexes bloat together, so their ratio stays
// meaningful on a table that has been pruned by DELETE.
func (s LogStorageStats) BytesPerRow() float64 {
	if s.AvgRowBytes <= 0 {
		return 0
	}
	ratio := 1.0
	if s.HeapBytes > 0 {
		ratio += float64(s.IndexBytes) / float64(s.HeapBytes)
	}
	return s.AvgRowBytes * ratio
}

// LiveBytes estimates the bytes held by live rows. It is what the budget
// compares against: after a DELETE the files keep their size but the
// freed space is reused by new inserts, so the table stops growing once
// LiveBytes is under budget.
func (s LogStorageStats) LiveBytes() int64 {
	return int64(float64(s.LiveRows) * s.BytesPerRow())
}

// LogStorageStats reads the catalog figures for the budget. Cheap: the
// size functions stat files, n_live_tup is a counter, and the width
// sample reads the newest rows through the primary key.
func (d *LogDB) LogStorageStats(ctx context.Context) (LogStorageStats, error) {
	var s LogStorageStats
	// pg_partition_tree lists nothing for a regular table, so the table
	// itself is unioned in; a partitioned parent has no storage of its own.
	err := d.DB.DB.QueryRowContext(ctx, `
		WITH rels AS (
			SELECT to_regclass('"LogLine"') AS relid
			UNION
			SELECT relid FROM pg_partition_tree(to_regclass('"LogLine"')) WHERE isleaf
		)
		SELECT COALESCE(SUM(pg_total_relation_size(r.relid)), 0),
		       COALESCE(SUM(pg_relation_size(r.relid)), 0),
		       COALESCE(SUM(pg_indexes_size(r.relid)), 0),
		       COALESCE(SUM(st.n_live_tup), 0),
		       pg_database_size(current_database())
		FROM rels r
		LEFT JOIN pg_stat_user_tables st ON st.relid = r.relid
		WHERE r.relid IS NOT NULL`).Scan(&s.TotalBytes, &s.HeapBytes, &s.IndexBytes, &s.LiveRows, &s.DatabaseBytes)
	if err != nil {
		return s, fmt.Errorf("log storage stats: %w", err)
	}
	if err := d.DB.DB.QueryRowContext(ctx, `
		SELECT COALESCE(AVG(pg_column_size(l.*)), 0)::float8
		FROM (SELECT * FROM "LogLine" ORDER BY "id" DESC LIMIT 1000) l`).Scan(&s.AvgRowBytes); err != nil {
		return s, fmt.Errorf("log row width: %w", err)
	}
	return s, nil
}

// OldestLogTs is the ts of the oldest stored line (zero when empty): the
// effective retention once the byte budget, not the day limit, is what
// prunes. min(ts) is served from LogLine_ts_idx, partitioned or not.
func (d *LogDB) OldestLogTs(ctx context.Context) (time.Time, error) {
	var ts sql.NullTime
	if err := d.DB.DB.QueryRowContext(ctx, `SELECT min("ts") FROM "LogLine"`).Scan(&ts); err != nil {
		return time.Time{}, fmt.Errorf("oldest log ts: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// HasLogTrigramIndex reports whether the pg_trgm search index exists.
func (d *LogDB) HasLogTrigramIndex(ctx context.Context) (bool, error) {
	var ok bool
	err := d.DB.DB.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, quoteIdent(logTrigramIndex)).Scan(&ok)
	return ok, err
}

// PruneOldestLogs deletes up to n of the oldest LogLine rows (by ts) in
// one short transaction and returns how many went. The budget enforcer
// calls it in small chunks so no single statement holds locks or
// generates WAL for long.
func (d *LogDB) PruneOldestLogs(ctx context.Context, n int) (int64, error) {
	if n <= 0 {
		return 0, nil
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("prune oldest logs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '2min'`); err != nil {
		return 0, fmt.Errorf("prune oldest logs: %w", err)
	}
	// (tableoid, ctid): a bare ctid is only unique within one partition.
	res, err := tx.ExecContext(ctx, `
DELETE FROM "LogLine"
WHERE (tableoid, ctid) IN (
  SELECT tableoid, ctid FROM "LogLine"
  ORDER BY ts ASC
  LIMIT $1
)`, n)
	if err != nil {
		return 0, fmt.Errorf("prune oldest logs: %w", err)
	}
	deleted, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("prune oldest logs: %w", err)
	}
	return deleted, nil
}
