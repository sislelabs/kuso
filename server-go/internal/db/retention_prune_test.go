package db

import (
	"context"
	"testing"
	"time"
)

// LOOP-25: EnvHint rows hold raw log text and never expired.
func TestPruneEnvHints(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	t.Cleanup(func() { _, _ = d.ExecContext(ctx, `DELETE FROM "EnvHint" WHERE "project" = 'prune-hints'`) })
	if err := d.UpsertEnvHints(ctx, []EnvHint{
		{Project: "prune-hints", Service: "s", Name: "OLD", LastLine: "x", LastSeen: time.Now().Add(-30 * 24 * time.Hour)},
		{Project: "prune-hints", Service: "s", Name: "NEW", LastLine: "y", LastSeen: time.Now()},
	}); err != nil {
		t.Fatal(err)
	}
	n, err := d.PruneEnvHints(ctx, time.Now().AddDate(0, 0, -7))
	if err != nil || n != 1 {
		t.Fatalf("PruneEnvHints = %d, %v; want 1", n, err)
	}
	left, _ := d.ListEnvHints(ctx, "prune-hints", "s")
	if len(left) != 1 || left[0].Name != "NEW" {
		t.Errorf("remaining hints = %+v, want only NEW", left)
	}
}

// LOOP-26: dead-letter outbox rows (full event JSON) were kept forever.
func TestPruneOutboxDeadLetters(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	mk := func(age time.Duration, attempts int) int64 {
		id, err := d.EnqueueOutbox(ctx, "ch", "x", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := d.ExecContext(ctx, `UPDATE "NotificationOutbox" SET "attempts"=$1, "createdAt"=$2 WHERE "id"=$3`,
			attempts, time.Now().Add(-age), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	mk(60*24*time.Hour, MaxOutboxAttempts) // old dead letter: pruned
	mk(2*24*time.Hour, MaxOutboxAttempts)  // recent dead letter: kept
	mk(60*24*time.Hour, 1)                 // old but still retrying: kept
	n, err := d.PruneOutboxDeadLetters(ctx, time.Now().AddDate(0, 0, -30))
	if err != nil || n != 1 {
		t.Fatalf("PruneOutboxDeadLetters = %d, %v; want 1", n, err)
	}
}
