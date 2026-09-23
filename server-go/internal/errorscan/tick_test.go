package errorscan

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"kuso/server/internal/db"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := d.DB.Exec(`TRUNCATE TABLE "LogLine", "ErrorEvent", "ErrorScannerState" RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

// seedLogLines inserts n lines with ids 1..n. Every id in errorIDs is an
// error line; the rest are benign.
func seedLogLines(t *testing.T, d *db.DB, n int, errorIDs ...int) {
	t.Helper()
	isErr := map[int]bool{}
	for _, id := range errorIDs {
		isErr[id] = true
	}
	for i := 1; i <= n; i++ {
		line := "GET /healthz 200"
		if isErr[i] {
			line = "ERROR boom"
		}
		if _, err := d.DB.Exec(`INSERT INTO "LogLine" (ts, pod, project, service, env, line) VALUES ($1, 'p', 'proj', 'proj-svc', 'production', $2)`,
			time.Now(), line); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func errorEventCount(t *testing.T, d *db.DB) int {
	t.Helper()
	var n int
	if err := d.DB.QueryRow(`SELECT count(*) FROM "ErrorEvent"`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

func watermark(t *testing.T, d *db.DB) int64 {
	t.Helper()
	wm, err := d.ScannerWatermark(context.Background(), watermarkKey)
	if err != nil {
		t.Fatalf("watermark: %v", err)
	}
	return wm
}

// Live, ingest outran one 500-row batch per 30s tick and the watermark sat
// 7 days behind. A tick must keep pulling batches until it is caught up.
func TestTick_DrainsBacklogAcrossBatches(t *testing.T) {
	d := openTestDB(t)
	seedLogLines(t, d, 95, 3, 50, 94)

	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 10}
	s.tick(context.Background())

	if got := watermark(t, d); got != 95 {
		t.Errorf("watermark = %d, want 95 (whole backlog in one tick)", got)
	}
	if got := errorEventCount(t, d); got != 3 {
		t.Errorf("ErrorEvent rows = %d, want 3", got)
	}
}

// The catch-up loop runs on the leader, so it must stay bounded per tick.
func TestTick_StopsAtMaxBatches(t *testing.T) {
	d := openTestDB(t)
	seedLogLines(t, d, 95)

	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 10, MaxBatchesPerTick: 3}
	s.tick(context.Background())

	if got := watermark(t, d); got != 30 {
		t.Errorf("watermark = %d, want 30 (3 batches of 10)", got)
	}
}

// A cursor further behind than MaxLag jumps to the recent window instead
// of grinding through lines that are about to be pruned.
func TestTick_SkipsAheadWhenLagExceedsMax(t *testing.T) {
	d := openTestDB(t)
	seedLogLines(t, d, 95, 5, 90)

	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 10, MaxLag: 20}
	s.tick(context.Background())

	if got := watermark(t, d); got != 95 {
		t.Errorf("watermark = %d, want 95", got)
	}
	var msgs []string
	rows, err := d.DB.Query(`SELECT message FROM "ErrorEvent"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var m string
		_ = rows.Scan(&m)
		msgs = append(msgs, m)
	}
	if len(msgs) != 1 {
		t.Errorf("ErrorEvent rows = %d, want 1 (id 5 skipped, id 90 scanned)", len(msgs))
	}
}
