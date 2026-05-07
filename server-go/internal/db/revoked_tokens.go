// Token revocation. JWTs are signature-verified and otherwise valid
// until they expire (default 10h); without a revocation list, logout
// is a UI gesture only and a leaked CLI token lives until the natural
// TTL. RevokedToken stores the JTI of every explicitly-revoked token
// so the auth middleware can reject it on the next request.
//
// Hot path: every authenticated request fires SeenRevoked. The
// "RevokedToken" PK is text (the jti) and the lookup is a single
// b-tree probe; on a typical pgx hot pool the round-trip is sub-ms.
// Worth it: the alternative is "leaked tokens are valid for 10h".
//
// Retention: rows are pruned once the token's expiresAt has passed
// — beyond that point the JWT itself is rejected by the signature
// layer, so storing the revocation row is wasted space.

package db

import (
	"context"
	"fmt"
	"time"
)

// RevokeToken persists a revocation row. Idempotent: re-revoking a
// jti updates expiresAt + reason so a later prune still picks it up.
// expiresAt should be the JWT's own exp claim — once past, the
// signature layer alone catches it and we can drop the row.
func (d *DB) RevokeToken(ctx context.Context, jti, userID, reason string, expiresAt time.Time) error {
	if jti == "" {
		return fmt.Errorf("db: revoke: empty jti")
	}
	if expiresAt.IsZero() {
		// Never-expiring token (kuso PAT "no expiry"). Pick a far-
		// future bound so the prune loop doesn't drop it until
		// 100y from now.
		expiresAt = time.Now().Add(100 * 365 * 24 * time.Hour)
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO "RevokedToken" ("jti", "userId", "reason", "expiresAt")
VALUES (?, ?, ?, ?)
ON CONFLICT ("jti") DO UPDATE SET
  "reason" = excluded."reason",
  "expiresAt" = excluded."expiresAt"`,
		jti, userID, reason, expiresAt.UTC(),
	)
	if err != nil {
		return fmt.Errorf("db: revoke token: %w", err)
	}
	return nil
}

// IsTokenRevoked is the auth middleware's hot-path probe. Returns
// true if the jti is present in RevokedToken; false otherwise (or
// on DB error — the middleware fail-opens so a transient outage
// doesn't 401 every request).
func (d *DB) IsTokenRevoked(ctx context.Context, jti string) bool {
	if jti == "" {
		return false
	}
	var present int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM "RevokedToken" WHERE "jti" = ? LIMIT 1`, jti,
	).Scan(&present)
	if err != nil {
		// sql.ErrNoRows is the happy "not revoked" path; any other
		// error is treated the same way (fail-open) — see comment
		// above.
		return false
	}
	return present == 1
}

// RevokeAllUserTokens revokes every JTI we know about for a user.
// Today the JTI universe is "rows in RevokedToken" — we don't track
// every issued JWT — so this is mostly useful when paired with the
// future ActiveToken table. Kept here as the obvious extension
// point so role-demotion handlers have a single function to call.
//
// Returns the number of rows touched.
func (d *DB) RevokeAllUserTokens(ctx context.Context, userID, reason string) (int64, error) {
	res, err := d.ExecContext(ctx, `
UPDATE "RevokedToken"
   SET "reason" = ?
 WHERE "userId" = ?`, reason, userID)
	if err != nil {
		return 0, fmt.Errorf("db: revoke all user tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// PruneRevokedTokens removes rows whose tokens have expired (and
// would therefore be rejected by the signature layer alone). Called
// from the daily cleanup goroutine.
func (d *DB) PruneRevokedTokens(ctx context.Context) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM "RevokedToken" WHERE "expiresAt" < ?`, time.Now().UTC(),
	)
	if err != nil {
		return 0, fmt.Errorf("db: prune revoked tokens: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
