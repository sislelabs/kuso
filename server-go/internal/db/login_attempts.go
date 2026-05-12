package db

import (
	"context"
	"time"
)

// LoginAttemptResult is what the limiter returns from a single
// "should I let this request through?" check.
type LoginAttemptResult struct {
	// Allowed is false when the IP has hit the cap inside its window.
	Allowed bool
	// RetryAfter is how long the caller must wait before the next
	// allowed attempt. Zero when Allowed is true.
	RetryAfter time.Duration
}

// AllowLoginAttempt is the atomic check-and-increment that backs the
// login rate limiter. INSERT-on-conflict semantics:
//
//   - First attempt for this IP in this window: count=1, resetAt=now+window.
//   - Subsequent attempt while still in window: count+=1.
//   - Attempt past the previous resetAt: reset count to 1, resetAt to now+window.
//
// One round-trip, no read-then-write race between replicas. Caller
// inspects Allowed + RetryAfter to decide 429 vs proceed.
func (d *DB) AllowLoginAttempt(ctx context.Context, ip string, max int, window time.Duration) (LoginAttemptResult, error) {
	now := time.Now()
	resetAt := now.Add(window)
	// The CASE preserves resetAt while we're still in the window and
	// rolls it forward when the previous window has elapsed. count
	// behaves the same way.
	const q = `
INSERT INTO "LoginAttempt"("ip", "count", "resetAt")
VALUES ($1, 1, $2)
ON CONFLICT ("ip") DO UPDATE SET
    "count"   = CASE WHEN "LoginAttempt"."resetAt" < $3 THEN 1 ELSE "LoginAttempt"."count" + 1 END,
    "resetAt" = CASE WHEN "LoginAttempt"."resetAt" < $3 THEN $2 ELSE "LoginAttempt"."resetAt" END
RETURNING "count", "resetAt"
`
	var count int
	var stored time.Time
	if err := d.QueryRowContext(ctx, q, ip, resetAt, now).Scan(&count, &stored); err != nil {
		return LoginAttemptResult{}, err
	}
	if count > max {
		return LoginAttemptResult{Allowed: false, RetryAfter: time.Until(stored)}, nil
	}
	return LoginAttemptResult{Allowed: true}, nil
}

// PruneLoginAttempts drops rows whose window has elapsed. Cheap
// because resetAt is indexed; safe to call on a slow ticker.
func (d *DB) PruneLoginAttempts(ctx context.Context) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "LoginAttempt" WHERE "resetAt" < CURRENT_TIMESTAMP`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ResetLoginAttemptsForTesting wipes the table. Tests that share a
// Postgres-backed test process call this between cases so the 8th
// login-flow test in a single run doesn't hit a leftover cap.
func (d *DB) ResetLoginAttemptsForTesting(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `DELETE FROM "LoginAttempt"`)
	return err
}
