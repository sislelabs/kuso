package db

import (
	"context"
	"testing"
	"time"
)

// TestLogDB_InsertSearch round-trips a log line through the v0.9
// Postgres-backed LogDB. FTS5 was dropped — search is now ILIKE so
// the query path is shorter; the contract (filter by project/service,
// match by substring) is unchanged.
func TestLogDB_InsertSearch(t *testing.T) {
	d := openTestDB(t).AsLogDB()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := d.InsertLogLines(ctx, []LogLine{
		{Ts: now, Pod: "pod-a", Project: "alpha", Service: "web", Env: "production", Line: "boot complete"},
		{Ts: now, Pod: "pod-a", Project: "alpha", Service: "web", Env: "production", Line: "request 200 OK"},
	}); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}

	rows, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Query: "boot"})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if len(rows) != 1 || rows[0].Line != "boot complete" {
		t.Errorf("SearchLogs: got %+v", rows)
	}

	rows, err = d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web"})
	if err != nil {
		t.Fatalf("SearchLogs metadata: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("SearchLogs metadata: got %d, want 2", len(rows))
	}

	n, err := d.CountLogMatches(ctx, "alpha", "web", "request", time.Time{})
	if err != nil {
		t.Fatalf("CountLogMatches: %v", err)
	}
	if n != 1 {
		t.Errorf("CountLogMatches: got %d, want 1", n)
	}

	deleted, err := d.PruneLogsOlderThan(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("PruneLogsOlderThan: %v", err)
	}
	if deleted != 2 {
		t.Errorf("PruneLogsOlderThan: deleted %d, want 2", deleted)
	}
}
