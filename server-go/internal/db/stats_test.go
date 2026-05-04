package db_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// Stats is the new busy/wait counter surface; the test exercises the
// invariants the admin endpoint depends on.

func TestStats_WriteCount_Increments(t *testing.T) {
	t.Parallel()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	before := d.Stats().WriteCount

	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES ('r1','t',datetime('now'),datetime('now'))`); err != nil {
		t.Fatalf("insert: %v", err)
	}
	after := d.Stats().WriteCount
	if after-before != 1 {
		t.Errorf("WriteCount delta=%d want 1", after-before)
	}
}

func TestStats_WriteCount_CountsErrors(t *testing.T) {
	t.Parallel()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Syntax error → exec returns err, but we still want to count it
	// (the wrapper times every attempt regardless of outcome).
	before := d.Stats().WriteCount
	_, _ = d.ExecContext(context.Background(), `INSERT INTO bogus_table_xyz VALUES (1)`)
	after := d.Stats().WriteCount
	if after-before != 1 {
		t.Errorf("WriteCount should count failed exec; delta=%d", after-before)
	}
}

func TestStats_BusyCount_StaysZeroWhenIdle(t *testing.T) {
	t.Parallel()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	for i := 0; i < 10; i++ {
		if _, err := d.ExecContext(context.Background(),
			`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES (?,?,datetime('now'),datetime('now'))`,
			"r"+string(rune('a'+i)), "name"+string(rune('a'+i))); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	if got := d.Stats().BusyCount; got != 0 {
		t.Errorf("BusyCount=%d on a single-writer test; want 0", got)
	}
}

// Snapshot must be value-copyable (the admin endpoint marshals it to
// JSON). Spot-check the math: avg = wait / count.
func TestStats_Snapshot_AverageMath(t *testing.T) {
	t.Parallel()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	for i := 0; i < 5; i++ {
		_, _ = d.ExecContext(context.Background(),
			`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES (?, 'x', datetime('now'), datetime('now'))`,
			"row"+string(rune('a'+i)))
	}
	s := d.Stats()
	if s.WriteCount < 5 {
		t.Fatalf("WriteCount=%d want >=5", s.WriteCount)
	}
	// avg ≤ total wait (by construction) and >= 0.
	if s.AvgWriteWaitMs < 0 || int64(s.WriteWaitMs) < s.AvgWriteWaitMs {
		t.Errorf("snapshot math broken: %+v", s)
	}
}

// Operating contract: a context-cancelled exec still counts as a
// write. Regression: if we accidentally short-circuit the increment
// path on cancel, busy spikes become invisible.
func TestStats_CountsCancelledExec(t *testing.T) {
	t.Parallel()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	before := d.Stats().WriteCount
	_, err = d.ExecContext(ctx, `INSERT INTO "Role" (id,name,"createdAt","updatedAt") VALUES ('rx','x',datetime('now'),datetime('now'))`)
	if err == nil {
		t.Skip("exec completed before nanosecond deadline; skip — non-deterministic")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Logf("err=%v (test continues — any err means we're on the failure path)", err)
	}
	if d.Stats().WriteCount-before != 1 {
		t.Errorf("WriteCount must increment even when exec is cancelled")
	}
}
