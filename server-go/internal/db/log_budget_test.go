package db

import (
	"context"
	"testing"
	"time"
)

// The byte-budget prune must take the OLDEST rows, and the stats it is
// driven by must see the table. PG-backed.
func TestPruneOldestLogs_DeletesOldestFirst(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `DELETE FROM "LogLine"`); err != nil {
		t.Fatal(err)
	}
	ldb := d.AsLogDB()
	now := time.Now().UTC()
	var rows []LogLine
	// Insert newest first so id order and ts order disagree.
	for i := 0; i < 50; i++ {
		rows = append(rows, LogLine{Ts: now.Add(-time.Duration(i) * time.Minute), Pod: "p", Project: "a", Service: "s", Line: "new"})
	}
	for i := 0; i < 30; i++ {
		rows = append(rows, LogLine{Ts: now.Add(-48*time.Hour - time.Duration(i)*time.Minute), Pod: "p", Project: "a", Service: "s", Line: "old"})
	}
	if err := ldb.InsertLogLines(ctx, rows); err != nil {
		t.Fatal(err)
	}

	st, err := ldb.LogStorageStats(ctx)
	if err != nil {
		t.Fatalf("LogStorageStats: %v", err)
	}
	if st.TotalBytes <= 0 || st.HeapBytes <= 0 || st.AvgRowBytes <= 0 || st.DatabaseBytes < st.TotalBytes {
		t.Fatalf("implausible stats %+v", st)
	}

	n, err := ldb.PruneOldestLogs(ctx, 30)
	if err != nil {
		t.Fatalf("PruneOldestLogs: %v", err)
	}
	if n != 30 {
		t.Fatalf("deleted %d, want 30", n)
	}
	var oldLeft, newLeft int
	if err := d.QueryRowContext(ctx, `SELECT
		COUNT(*) FILTER (WHERE line = 'old'),
		COUNT(*) FILTER (WHERE line = 'new') FROM "LogLine"`).Scan(&oldLeft, &newLeft); err != nil {
		t.Fatal(err)
	}
	if oldLeft != 0 || newLeft != 50 {
		t.Fatalf("after prune old=%d new=%d, want old=0 new=50", oldLeft, newLeft)
	}
}
