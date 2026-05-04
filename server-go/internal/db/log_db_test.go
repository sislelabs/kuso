package db

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestLogDB_OpenInsertSearch round-trips a log line through OpenLog,
// InsertLogLines, and SearchLogs to make sure the schema, FTS5
// triggers, and search path all wire up against the dedicated file.
func TestLogDB_OpenInsertSearch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.sqlite")
	d, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx := context.Background()
	now := time.Now().UTC()
	if err := d.InsertLogLines(ctx, []LogLine{
		{Ts: now, Pod: "pod-a", Project: "alpha", Service: "web", Env: "production", Line: "boot complete"},
		{Ts: now, Pod: "pod-a", Project: "alpha", Service: "web", Env: "production", Line: "request 200 OK"},
	}); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}

	// FTS5 round-trip — search on the inserted text.
	rows, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Query: "boot"})
	if err != nil {
		t.Fatalf("SearchLogs: %v", err)
	}
	if len(rows) != 1 || rows[0].Line != "boot complete" {
		t.Errorf("SearchLogs: got %+v", rows)
	}

	// Metadata-only path (no FTS) — just project + service filter.
	rows, err = d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web"})
	if err != nil {
		t.Fatalf("SearchLogs metadata: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("SearchLogs metadata: got %d, want 2", len(rows))
	}

	// Count helper used by the alert engine.
	n, err := d.CountLogMatches(ctx, "alpha", "web", "request", time.Time{})
	if err != nil {
		t.Fatalf("CountLogMatches: %v", err)
	}
	if n != 1 {
		t.Errorf("CountLogMatches: got %d, want 1", n)
	}

	// Prune everything older than 1 second from now (i.e. all rows
	// just inserted, since they're stamped with `now`).
	deleted, err := d.PruneLogsOlderThan(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatalf("PruneLogsOlderThan: %v", err)
	}
	if deleted != 2 {
		t.Errorf("PruneLogsOlderThan: deleted %d, want 2", deleted)
	}
}

// TestLogDB_Reopen verifies the schema is idempotent — opening the
// same file twice doesn't error on duplicate CREATE TABLE.
func TestLogDB_Reopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "logs.sqlite")
	d1, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog 1: %v", err)
	}
	if err := d1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	d2, err := OpenLog(path)
	if err != nil {
		t.Fatalf("OpenLog 2 (reopen): %v", err)
	}
	if err := d2.Close(); err != nil {
		t.Fatalf("Close 2: %v", err)
	}
}
