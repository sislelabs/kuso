package db

import (
	"context"
	"fmt"
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

// TestLogDB_SearchLogsPaging covers scrollback past the first page.
// SearchLogs returns newest-first with no cursor, so the UI could only
// ever show the most recent page (200 lines) and "scroll to the start
// of the logs" was impossible. BeforeID is a keyset cursor: it pages on
// the primary key rather than OFFSET, so concurrent inserts at the head
// can't shift rows across a page boundary and duplicate or skip lines.
func TestLogDB_SearchLogsPaging(t *testing.T) {
	d := openTestDB(t).AsLogDB()
	ctx := context.Background()
	now := time.Now().UTC()

	lines := make([]LogLine, 0, 25)
	for i := 0; i < 25; i++ {
		lines = append(lines, LogLine{
			Ts: now.Add(time.Duration(i) * time.Second), Pod: "pod-a",
			Project: "alpha", Service: "web", Env: "production",
			Line: fmt.Sprintf("line %02d", i),
		})
	}
	if err := d.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}

	page1, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Limit: 10})
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 10 {
		t.Fatalf("page1: got %d rows, want 10", len(page1))
	}
	if page1[0].Line != "line 24" {
		t.Errorf("page1 newest-first broken: got %q, want %q", page1[0].Line, "line 24")
	}

	// Walk backwards through every page via the cursor.
	seen := map[string]bool{}
	for _, r := range page1 {
		seen[r.Line] = true
	}
	cursor := page1[len(page1)-1].ID
	for pages := 0; pages < 10; pages++ {
		next, err := d.SearchLogs(ctx, SearchLogsRequest{
			Project: "alpha", Service: "web", Limit: 10, BeforeID: cursor,
		})
		if err != nil {
			t.Fatalf("page after %d: %v", cursor, err)
		}
		if len(next) == 0 {
			break
		}
		for _, r := range next {
			if seen[r.Line] {
				t.Errorf("duplicate row across pages: %q", r.Line)
			}
			seen[r.Line] = true
		}
		if next[len(next)-1].ID >= cursor {
			t.Fatalf("cursor did not advance: %d >= %d", next[len(next)-1].ID, cursor)
		}
		cursor = next[len(next)-1].ID
	}

	// Paging must reach the very first line, which is the whole point.
	if len(seen) != 25 {
		t.Errorf("paged %d distinct lines, want 25 (cannot scroll to start)", len(seen))
	}
	if !seen["line 00"] {
		t.Error("never reached the oldest line; scrollback is still truncated")
	}

	// A cursor must not silently widen the page past the clamp.
	big, err := d.SearchLogs(ctx, SearchLogsRequest{Project: "alpha", Service: "web", Limit: 10_000})
	if err != nil {
		t.Fatalf("clamp: %v", err)
	}
	if len(big) != 25 {
		t.Errorf("clamp: got %d, want all 25", len(big))
	}
}
