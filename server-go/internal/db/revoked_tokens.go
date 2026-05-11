// Token revocation has two layers, both queried by the auth
// middleware on every authenticated request:
//
//   RevokedToken           — kill ONE specific jti (explicit logout,
//                            user clicked "revoke" on a personal
//                            access token). PK on jti, exact-match
//                            probe.
//
//   UserTokenInvalidation  — kill EVERY token currently issued to a
//                            user (role demotion, group removal,
//                            deactivation, password reset). One row
//                            per user; we compare iat to the
//                            watermark — any JWT older than the
//                            watermark is treated as revoked.
//
// Both probes are sub-millisecond on a hot pool. Worth it: without
// either one, leaked tokens or stale-claim tokens stay valid for
// the full 10h TTL.
//
// Retention:
//   RevokedToken — pruned once expiresAt has passed; the signature
//                  layer rejects expired tokens on its own.
//   UserTokenInvalidation — never pruned; a tombstone is cheap, and
//                  removing one would re-validate older tokens for
//                  that user.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RevokeToken persists a revocation row for a single jti. Idempotent:
// re-revoking updates expiresAt + reason.
func (d *DB) RevokeToken(ctx context.Context, jti, userID, reason string, expiresAt time.Time) error {
	if jti == "" {
		return fmt.Errorf("db: revoke: empty jti")
	}
	if expiresAt.IsZero() {
		// Never-expiring token (kuso PAT "no expiry") — pick a far-
		// future bound so the prune loop doesn't drop the row.
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

// IsTokenRevoked is the per-jti probe. Hot path on every
// authenticated request.
//
// Returns (revoked, err). err is nil when the row genuinely doesn't
// exist (ErrNoRows is squashed). The caller decides fail-open vs
// fail-closed on err != nil — most callers should fail closed but
// can layer a short cache of last-known-good answers in front to ride
// out brief DB blips without 401-ing every user.
func (d *DB) IsTokenRevoked(ctx context.Context, jti string) (bool, error) {
	if jti == "" {
		return false, nil
	}
	var present int
	err := d.QueryRowContext(ctx,
		`SELECT 1 FROM "RevokedToken" WHERE "jti" = ? LIMIT 1`, jti,
	).Scan(&present)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return present == 1, nil
}

// InvalidateUserTokens bumps the per-user "valid-from" watermark to
// `at` (typically time.Now()). Every JWT issued before that moment
// is rejected by the auth middleware on next use, regardless of jti.
//
// Use cases:
//   - role demotion: user was admin, now viewer; their old admin-
//     claim JWT must die immediately.
//   - group removal: same logic at the group layer.
//   - user deactivation / deletion: kill everything.
//   - password change: belt-and-braces — old token survives a
//     password change in JWT-only auth, this closes that window.
func (d *DB) InvalidateUserTokens(ctx context.Context, userID, reason string, at time.Time) error {
	if userID == "" {
		return fmt.Errorf("db: invalidate user tokens: empty userId")
	}
	if at.IsZero() {
		at = time.Now()
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO "UserTokenInvalidation" ("userId", "invalidatedBefore", "reason", "updatedAt")
VALUES (?, ?, ?, ?)
ON CONFLICT ("userId") DO UPDATE SET
  "invalidatedBefore" = excluded."invalidatedBefore",
  "reason" = excluded."reason",
  "updatedAt" = excluded."updatedAt"`,
		userID, at.UTC(), reason, at.UTC(),
	)
	if err != nil {
		return fmt.Errorf("db: invalidate user tokens: %w", err)
	}
	d.EvictUserTenancy(userID)
	return nil
}

// UserTokenWatermark returns the time before which all tokens for
// `userID` are considered revoked. Zero time means no watermark set
// (i.e. all tokens valid as far as this layer is concerned). Hot
// path on every authenticated request — the auth middleware compares
// the JWT's iat to this value.
//
// Returns (watermark, err). err is nil when the user simply has no
// watermark row. The caller chooses fail-open vs fail-closed; the
// middleware layers a per-user TTL cache in front so transient DB
// blips don't 401 every active session.
func (d *DB) UserTokenWatermark(ctx context.Context, userID string) (time.Time, error) {
	if userID == "" {
		return time.Time{}, nil
	}
	var t sql.NullTime
	err := d.QueryRowContext(ctx,
		`SELECT "invalidatedBefore" FROM "UserTokenInvalidation" WHERE "userId" = ?`,
		userID,
	).Scan(&t)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, nil
		}
		return time.Time{}, err
	}
	if !t.Valid {
		return time.Time{}, nil
	}
	return t.Time, nil
}

// InvalidateUsersByRole bumps the watermark for every user assigned
// to `roleID`, with a single round-trip. Used when a Role's
// permission set is edited or the Role itself is deleted — every
// JWT that hard-coded the old permissions list must die.
func (d *DB) InvalidateUsersByRole(ctx context.Context, roleID, reason string) (int64, error) {
	if roleID == "" {
		return 0, nil
	}
	now := time.Now().UTC()
	res, err := d.ExecContext(ctx, `
INSERT INTO "UserTokenInvalidation" ("userId", "invalidatedBefore", "reason", "updatedAt")
SELECT u."id", ?, ?, ?
FROM "User" u
WHERE u."roleId" = ?
ON CONFLICT ("userId") DO UPDATE SET
  "invalidatedBefore" = excluded."invalidatedBefore",
  "reason" = excluded."reason",
  "updatedAt" = excluded."updatedAt"`,
		now, reason, now, roleID,
	)
	if err != nil {
		return 0, fmt.Errorf("db: invalidate users by role: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		d.EvictAllTenancy()
	}
	return n, nil
}

// InvalidateUsersByGroup is the group-membership analogue. Called
// when a UserGroup's projectMemberships JSON is edited or the group
// itself is deleted.
func (d *DB) InvalidateUsersByGroup(ctx context.Context, groupID, reason string) (int64, error) {
	if groupID == "" {
		return 0, nil
	}
	now := time.Now().UTC()
	res, err := d.ExecContext(ctx, `
INSERT INTO "UserTokenInvalidation" ("userId", "invalidatedBefore", "reason", "updatedAt")
SELECT m."A", ?, ?, ?
FROM "_UserToUserGroup" m
WHERE m."B" = ?
ON CONFLICT ("userId") DO UPDATE SET
  "invalidatedBefore" = excluded."invalidatedBefore",
  "reason" = excluded."reason",
  "updatedAt" = excluded."updatedAt"`,
		now, reason, now, groupID,
	)
	if err != nil {
		return 0, fmt.Errorf("db: invalidate users by group: %w", err)
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		d.EvictAllTenancy()
	}
	return n, nil
}

// PruneRevokedTokens removes per-jti rows whose tokens have expired.
// Called from the daily cleanup goroutine.
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
