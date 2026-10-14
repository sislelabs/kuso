package db

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// TestPruneLogsOlderThan_PartitionedKeepsFreshRows is the DB-1
// regression: on a partitioned LogLine a bare `ctid IN (…)` matched the
// same (block,offset) in every day-partition, so pruning old rows also
// deleted today's rows sitting at the same tids. PG-backed.
func TestPruneLogsOlderThan_PartitionedKeepsFreshRows(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	if err := d.MigrateLogLineToPartitioned(ctx, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("MigrateLogLineToPartitioned: %v", err)
	}
	// Put the shared test DB back to a regular LogLine for later tests.
	t.Cleanup(func() { restoreUnpartitionedLogLine(t, d) })
	st, err := d.LogPartitionState(ctx)
	if err != nil || !st.Partitioned {
		t.Fatalf("expected partitioned LogLine, got %+v err=%v", st, err)
	}

	now := time.Now().UTC()
	old := now.AddDate(0, 0, -2)
	if err := d.EnsureLogPartitionForDay(ctx, old); err != nil {
		t.Fatalf("ensure old partition: %v", err)
	}
	ldb := d.AsLogDB()
	var oldRows, freshRows []LogLine
	for i := 0; i < 100; i++ {
		oldRows = append(oldRows, LogLine{Ts: old, Pod: "p", Project: "a", Service: "s", Line: "old"})
		freshRows = append(freshRows, LogLine{Ts: now, Pod: "p", Project: "a", Service: "s", Line: "fresh"})
	}
	if err := ldb.InsertLogLines(ctx, oldRows); err != nil {
		t.Fatalf("insert old: %v", err)
	}
	if err := ldb.InsertLogLines(ctx, freshRows); err != nil {
		t.Fatalf("insert fresh: %v", err)
	}

	n, err := ldb.PruneLogsOlderThan(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("PruneLogsOlderThan: %v", err)
	}
	if n != 100 {
		t.Errorf("deleted %d rows, want 100 (only the old ones)", n)
	}
	var fresh, stale int
	if err := d.QueryRowContext(ctx, `SELECT
		COUNT(*) FILTER (WHERE line = 'fresh'),
		COUNT(*) FILTER (WHERE line = 'old')
		FROM "LogLine"`).Scan(&fresh, &stale); err != nil {
		t.Fatalf("count: %v", err)
	}
	if fresh != 100 || stale != 0 {
		t.Errorf("after prune fresh=%d old=%d, want fresh=100 old=0", fresh, stale)
	}
}
