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
		// Old enough that the scanner doesn't hold the chain open.
		if _, err := d.DB.Exec(`INSERT INTO "LogLine" (ts, pod, project, service, env, line) VALUES ($1, 'p', 'proj', 'proj-svc', 'production', $2)`,
			time.Now().Add(-time.Minute), line); err != nil {
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

func insertLine(t *testing.T, d *db.DB, ts time.Time, pod, line string) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO "LogLine" (ts, pod, project, service, env, line) VALUES ($1, $2, 'shop', 'shop-worker', 'production', $3)`,
		ts, pod, line); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func errorMessages(t *testing.T, d *db.DB) []string {
	t.Helper()
	rows, err := d.DB.Query(`SELECT message FROM "ErrorEvent" ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// A traceback split across two logship inserts, with a scan in between,
// used to become "failed to send order receipt" plus a separate
// "smtplib.SMTPServerDisconnected" group.
func TestTick_HoldsChainSplitAcrossInserts(t *testing.T) {
	d := openTestDB(t)
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	insertLine(t, d, t0, "p", "2026-11-09T10:00:00Z ERROR failed to send order receipt order_id=1012")
	insertLine(t, d, t0.Add(time.Millisecond), "p", "Traceback (most recent call last):")

	now := t0.Add(time.Second)
	s := &Scanner{DB: d, Logger: slog.Default(), now: func() time.Time { return now }}
	s.tick(context.Background())
	if got := errorEventCount(t, d); got != 0 {
		t.Fatalf("recorded %d events while the chain may still grow", got)
	}
	if got := watermark(t, d); got != 0 {
		t.Errorf("watermark = %d, want 0 (just before the held chain)", got)
	}

	insertLine(t, d, t0.Add(2*time.Millisecond), "p", `  File "/app/jobs/emails.py", line 42, in send_receipt`)
	insertLine(t, d, t0.Add(3*time.Millisecond), "p", "    smtp.send(msg)")
	insertLine(t, d, t0.Add(4*time.Millisecond), "p", "smtplib.SMTPServerDisconnected: Connection unexpectedly closed")
	now = t0.Add(30 * time.Second)
	s.tick(context.Background())

	got := errorMessages(t, d)
	if len(got) != 1 || got[0] != "smtplib.SMTPServerDisconnected: Connection unexpectedly closed" {
		t.Fatalf("events = %q, want the one SMTP error", got)
	}
	if wm := watermark(t, d); wm != 5 {
		t.Errorf("watermark = %d, want 5", wm)
	}
}

// A batch boundary inside a stack trace doesn't cut it either.
func TestTick_ChainSurvivesBatchBoundary(t *testing.T) {
	d := openTestDB(t)
	t0 := time.Now().Add(-time.Minute)
	insertLine(t, d, t0, "p", "ERROR payment webhook failed")
	insertLine(t, d, t0.Add(time.Millisecond), "p", "    at processWebhook (/app/jobs/payments.go:88)")
	insertLine(t, d, t0.Add(2*time.Millisecond), "p", "    at runJob (/app/queue/worker.go:141)")

	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 2}
	s.tick(context.Background())

	if got := errorMessages(t, d); len(got) != 1 {
		t.Fatalf("events = %q, want 1", got)
	}
}

// A new process (restart, leader change) resumes before a chain the old
// one held, re-reading lines whose chains it already recorded.
func TestTick_ResumeAfterRestartDoesNotDuplicate(t *testing.T) {
	d := openTestDB(t)
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	insertLine(t, d, t0, "held", "ERROR still being written")
	insertLine(t, d, t0.Add(2*time.Second), "done", "ERROR finished error")
	insertLine(t, d, t0.Add(10*time.Second), "done", "INFO next line")

	// Scanned 1s after "held": it stays open, while "done" was closed by
	// the line after it.
	now := t0.Add(time.Second)
	first := &Scanner{DB: d, Logger: slog.Default(), now: func() time.Time { return now }}
	first.tick(context.Background())
	if got := errorMessages(t, d); len(got) != 1 || got[0] != "finished error" {
		t.Fatalf("first process events = %q", got)
	}

	now = t0.Add(time.Minute)
	second := &Scanner{DB: d, Logger: slog.Default(), now: func() time.Time { return now }}
	second.tick(context.Background())
	got := errorMessages(t, d)
	if len(got) != 2 || got[1] != "still being written" {
		t.Fatalf("events after restart = %q, want finished + held, once each", got)
	}
}

// With MaxBatchSize set, batches grow with the lag so a bounded number of
// batches still drains the backlog (SCL-5).
func TestTick_BatchesScaleWithBacklog(t *testing.T) {
	d := openTestDB(t)
	seedLogLines(t, d, 95, 7, 93)

	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 10, MaxBatchSize: 100, MaxBatchesPerTick: 3}
	s.tick(context.Background())

	if got := watermark(t, d); got != 95 {
		t.Errorf("watermark = %d, want 95 (3 scaled batches cover the lag)", got)
	}
	if got := errorEventCount(t, d); got != 2 {
		t.Errorf("ErrorEvent rows = %d, want 2", got)
	}
}

// A skip-ahead is reported, not just logged.
func TestTick_SkipAheadCallsOnSkip(t *testing.T) {
	d := openTestDB(t)
	seedLogLines(t, d, 95)

	var skipped int64
	s := &Scanner{DB: d, Logger: slog.Default(), BatchSize: 10, MaxLag: 20,
		OnSkip: func(_ context.Context, n int64) { skipped += n }}
	s.tick(context.Background())

	if skipped != 75 {
		t.Errorf("OnSkip got %d skipped rows, want 75", skipped)
	}
}
