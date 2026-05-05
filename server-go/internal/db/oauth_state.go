// OAuth state nonce store. Per the v0.8.13 audit (S6), the OAuth
// state was a cookie-only value with no server-side single-use store
// — replayable for the cookie's full TTL. We persist each minted
// state with a `consumed` flag so the callback handler can reject
// duplicates.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrStateAlreadyConsumed is returned by ConsumeOAuthState when the
// state has been used already. Callers map this to a 400 with a
// "state already used or expired" body.
var ErrStateAlreadyConsumed = errors.New("oauth state already consumed")

// MintOAuthState records a freshly-issued state value with a TTL.
// `redirectTo` is the post-callback target (optional; some flows
// pass the user's original URL through). Returns nil on success;
// duplicate state values (a UUID collision the size of a galaxy)
// return ErrStateAlreadyConsumed which the caller should retry.
func (d *DB) MintOAuthState(ctx context.Context, state, redirectTo string) error {
	if state == "" {
		return fmt.Errorf("MintOAuthState: empty state")
	}
	_, err := d.ExecContext(ctx,
		`INSERT INTO "OAuthState" (state, "createdAt", consumed, "redirectTo")
		 VALUES (?, ?, false, ?)`,
		state, time.Now().UTC(), redirectTo,
	)
	if err != nil {
		// Postgres unique-violation surfaces with code 23505 — caller
		// can retry with a fresh state. We return a generic error so
		// the unique constraint isn't part of the API surface.
		return fmt.Errorf("MintOAuthState: %w", err)
	}
	return nil
}

// ConsumeOAuthState atomically marks a state as consumed. Returns
// ErrStateAlreadyConsumed if it was already consumed (replay) or
// missing (forged / expired). Returns sql.ErrNoRows-equivalent
// behaviour by also detecting "no row affected" — Postgres reports
// the same shape regardless of whether the row exists.
//
// We accept states up to `maxAge` old; older rows are ignored as
// expired. Pruning happens in the daily cleanup goroutine.
func (d *DB) ConsumeOAuthState(ctx context.Context, state string, maxAge time.Duration) error {
	if state == "" {
		return ErrStateAlreadyConsumed
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	res, err := d.ExecContext(ctx,
		`UPDATE "OAuthState"
		   SET consumed = true
		 WHERE state = ?
		   AND consumed = false
		   AND "createdAt" >= ?`,
		state, cutoff,
	)
	if err != nil {
		return fmt.Errorf("ConsumeOAuthState: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ConsumeOAuthState rows: %w", err)
	}
	if n == 0 {
		return ErrStateAlreadyConsumed
	}
	return nil
}

// PruneOAuthStates deletes rows older than `before`. Called from the
// daily cleanup goroutine to keep the table small.
func (d *DB) PruneOAuthStates(ctx context.Context, before time.Time) (int, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "OAuthState" WHERE "createdAt" < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("PruneOAuthStates: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// silence unused-import on sql when this file is compiled in
// isolation. Used through error sentinels in callers.
var _ = sql.ErrNoRows
