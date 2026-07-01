package db

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestOutboxEnqueueClaimDeliver runs the happy path: enqueue one
// row, claim it (verifies SKIP LOCKED works under concurrent
// callers), mark delivered. Re-claim should now return ErrNoOutboxRow.
func TestOutboxEnqueueClaimDeliver(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	id, err := d.EnqueueOutbox(ctx, "channel-1", "build.succeeded", []byte(`{"type":"build.succeeded"}`))
	if err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	if id == 0 {
		t.Fatal("expected non-zero row id")
	}

	row, err := d.ClaimOutboxRow(ctx)
	if err != nil {
		t.Fatalf("ClaimOutboxRow: %v", err)
	}
	if row.ID != id {
		t.Errorf("claimed wrong row: got %d, want %d", row.ID, id)
	}
	if row.NotificationID != "channel-1" {
		t.Errorf("notification id = %q, want channel-1", row.NotificationID)
	}
	if row.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (fresh row)", row.Attempts)
	}

	// The claim committed with a lease pushing nextAttemptAt into the
	// future, so a concurrent worker must not re-claim mid-delivery.
	if _, err := d.ClaimOutboxRow(ctx); !errors.Is(err, ErrNoOutboxRow) {
		t.Errorf("re-claim while leased should be ErrNoOutboxRow, got %v", err)
	}

	if err := d.MarkOutboxDelivered(ctx, row.ID); err != nil {
		t.Fatalf("MarkOutboxDelivered: %v", err)
	}

	// Now the queue should be empty.
	_, err = d.ClaimOutboxRow(ctx)
	if !errors.Is(err, ErrNoOutboxRow) {
		t.Errorf("re-claim after delivered should be ErrNoOutboxRow, got %v", err)
	}
}

// TestOutboxAttemptBumpsScheduling verifies the retry path stamps
// attempts++ + nextAttemptAt and prevents re-claim until the
// scheduled time passes.
func TestOutboxAttemptBumpsScheduling(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	id, err := d.EnqueueOutbox(ctx, "channel-1", "build.failed", []byte(`{"type":"build.failed"}`))
	if err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}

	row, err := d.ClaimOutboxRow(ctx)
	if err != nil {
		t.Fatalf("ClaimOutboxRow: %v", err)
	}
	future := time.Now().Add(10 * time.Minute)
	if err := d.MarkOutboxAttempt(ctx, row.ID, "test failure", future); err != nil {
		t.Fatalf("MarkOutboxAttempt: %v", err)
	}

	// Row exists with attempts=1, nextAttemptAt in the future →
	// re-claim should not pick it up.
	_, err = d.ClaimOutboxRow(ctx)
	if !errors.Is(err, ErrNoOutboxRow) {
		t.Errorf("re-claim before nextAttemptAt should be ErrNoOutboxRow, got %v", err)
	}

	// Verify the row state via a manual SELECT.
	row2 := d.QueryRowContext(ctx, `SELECT "attempts", "lastError" FROM "NotificationOutbox" WHERE "id" = $1`, id)
	var attempts int
	var lastErr string
	if err := row2.Scan(&attempts, &lastErr); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if attempts != 1 {
		t.Errorf("attempts = %d, want 1", attempts)
	}
	if lastErr != "test failure" {
		t.Errorf("lastError = %q, want test failure", lastErr)
	}
}

// TestOutboxCapStopsClaim verifies rows past MaxOutboxAttempts are
// skipped by future claims — they're dead-letter, owned by the
// operator dashboard.
func TestOutboxCapStopsClaim(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	id, err := d.EnqueueOutbox(ctx, "channel-x", "x", []byte(`{}`))
	if err != nil {
		t.Fatalf("EnqueueOutbox: %v", err)
	}
	// Force attempts to the cap.
	if _, err := d.DB.ExecContext(ctx, `UPDATE "NotificationOutbox" SET "attempts" = $1 WHERE "id" = $2`, MaxOutboxAttempts, id); err != nil {
		t.Fatalf("force attempts: %v", err)
	}
	// Claim should refuse — row is dead-letter.
	_, err = d.ClaimOutboxRow(ctx)
	if !errors.Is(err, ErrNoOutboxRow) {
		t.Errorf("claim of capped row should be ErrNoOutboxRow, got %v", err)
	}

	// CountOutboxDead reports the row.
	n, err := d.CountOutboxDead(ctx)
	if err != nil {
		t.Fatalf("CountOutboxDead: %v", err)
	}
	if n != 1 {
		t.Errorf("dead count = %d, want 1", n)
	}
	// Pending count should be 0 (the row is past the cap, so it's
	// not pending — that's the whole point of the partial index).
	pendingN, err := d.CountOutboxPending(ctx)
	if err != nil {
		t.Fatalf("CountOutboxPending: %v", err)
	}
	if pendingN != 0 {
		t.Errorf("pending count = %d, want 0", pendingN)
	}
}

// TestOutboxPruneDelivered drops successfully-delivered rows past
// the cutoff. Dead-letter rows (no deliveredAt) stay forever — the
// audit trail belongs to the operator.
func TestOutboxPruneDelivered(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// Old delivered row.
	idOld, err := d.EnqueueOutbox(ctx, "channel-a", "x", []byte(`{}`))
	if err != nil {
		t.Fatalf("enqueue old: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE "NotificationOutbox" SET "deliveredAt" = $1, "createdAt" = $2 WHERE "id" = $3`,
		time.Now().Add(-30*24*time.Hour), time.Now().Add(-30*24*time.Hour), idOld,
	); err != nil {
		t.Fatalf("backdate old: %v", err)
	}

	// Recent delivered row.
	idNew, err := d.EnqueueOutbox(ctx, "channel-b", "y", []byte(`{}`))
	if err != nil {
		t.Fatalf("enqueue new: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE "NotificationOutbox" SET "deliveredAt" = CURRENT_TIMESTAMP WHERE "id" = $1`, idNew,
	); err != nil {
		t.Fatalf("deliver new: %v", err)
	}

	// Dead-letter row (no deliveredAt, attempts at cap).
	idDead, err := d.EnqueueOutbox(ctx, "channel-c", "z", []byte(`{}`))
	if err != nil {
		t.Fatalf("enqueue dead: %v", err)
	}
	if _, err := d.DB.ExecContext(ctx,
		`UPDATE "NotificationOutbox" SET "attempts" = $1, "createdAt" = $2 WHERE "id" = $3`,
		MaxOutboxAttempts, time.Now().Add(-30*24*time.Hour), idDead,
	); err != nil {
		t.Fatalf("force dead: %v", err)
	}

	// Prune anything delivered before 7 days ago.
	n, err := d.PruneOutboxDelivered(ctx, time.Now().AddDate(0, 0, -7))
	if err != nil {
		t.Fatalf("PruneOutboxDelivered: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1 (the 30-day-old delivered row)", n)
	}

	// Verify state: new delivered row + dead-letter row still present,
	// old delivered row gone.
	var count int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "NotificationOutbox"`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("rows after prune = %d, want 2 (recent delivered + dead-letter)", count)
	}
}
