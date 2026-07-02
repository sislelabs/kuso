package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Token mirrors the shape the TS server returns from /api/tokens/my,
// minus the fields the client doesn't read.
type Token struct {
	ID        string
	Name      sql.NullString
	UserID    string
	ExpiresAt time.Time
	IsActive  bool
	LastUsed  sql.NullTime
	LastIP    sql.NullString
	CreatedAt time.Time
}

// CreateToken inserts a new Token row. The token's value lives only in
// the bearer JWT — the DB row is the audit + revocation surface, NOT
// the token material itself, matching the TS scheme.
func (d *DB) CreateToken(ctx context.Context, t *Token) error {
	if t.ID == "" {
		return errors.New("db: token id required")
	}
	now := prismaNow()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now.Time
	}
	_, err := d.ExecContext(ctx, `
INSERT INTO "Token" (id, name, "userId", "expiresAt", "isActive", role, groups, "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, $5, '', '', $6, $7)`,
		t.ID, t.Name, t.UserID, prismaAt(t.ExpiresAt), t.IsActive, prismaAt(t.CreatedAt), now,
	)
	if err != nil {
		return fmt.Errorf("db: create token: %w", err)
	}
	return nil
}

// ListTokensForUser returns the user's tokens, newest first.
func (d *DB) ListTokensForUser(ctx context.Context, userID string) ([]Token, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, name, "userId", "expiresAt", "isActive", "lastUsed", "lastIp", "createdAt"
FROM "Token" WHERE "userId" = $1 ORDER BY "createdAt" DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		var expiresAt, createdAt prismaTime
		var lastUsed nullPrismaTime
		if err := rows.Scan(&t.ID, &t.Name, &t.UserID, &expiresAt, &t.IsActive, &lastUsed, &t.LastIP, &createdAt); err != nil {
			return nil, fmt.Errorf("db: scan token: %w", err)
		}
		t.ExpiresAt = expiresAt.Time
		t.CreatedAt = createdAt.Time
		t.LastUsed = sql.NullTime{Time: lastUsed.Time, Valid: lastUsed.Valid}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteUserToken removes a single token belonging to userID. Refusing
// to delete cross-user tokens is the call site's job — for the /my/:id
// route it's enforced by the WHERE clause itself.
//
// SECURITY: deleting the Token row alone does NOT stop the bearer JWT —
// the auth middleware validates signature+expiry+revocation, never the
// row's existence. So we also write a RevokedToken row keyed on the
// token's jti (which equals the Token row id; see CreateMyToken /
// IssueForUser) in the SAME transaction, so a "revoked" token stops
// authenticating immediately instead of surviving until natural expiry.
func (d *DB) DeleteUserToken(ctx context.Context, userID, tokenID string) error {
	return d.deleteAndRevokeToken(ctx, `DELETE FROM "Token" WHERE id = $1 AND "userId" = $2 RETURNING "expiresAt"`,
		tokenID, tokenID, userID)
}

// AdminToken includes the username for the admin all-users list view.
type AdminToken struct {
	ID        string
	Name      sql.NullString
	UserID    string
	Username  string
	Email     string
	ExpiresAt time.Time
	IsActive  bool
	LastUsed  sql.NullTime
	CreatedAt time.Time
}

// ListAllTokens returns every token row joined with the owner's
// username + email. Admin-only — the slim shape the /api/tokens
// management page reads.
func (d *DB) ListAllTokens(ctx context.Context) ([]AdminToken, error) {
	rows, err := d.QueryContext(ctx, `
SELECT t.id, t.name, t."userId", u.username, u.email, t."expiresAt", t."isActive", t."lastUsed", t."createdAt"
FROM "Token" t JOIN "User" u ON u.id = t."userId"
ORDER BY t."createdAt" DESC`)
	if err != nil {
		return nil, fmt.Errorf("db: list all tokens: %w", err)
	}
	defer rows.Close()
	var out []AdminToken
	for rows.Next() {
		var a AdminToken
		var expiresAt, createdAt prismaTime
		var lastUsed nullPrismaTime
		if err := rows.Scan(&a.ID, &a.Name, &a.UserID, &a.Username, &a.Email, &expiresAt, &a.IsActive, &lastUsed, &createdAt); err != nil {
			return nil, err
		}
		a.ExpiresAt = expiresAt.Time
		a.CreatedAt = createdAt.Time
		a.LastUsed = sql.NullTime{Time: lastUsed.Time, Valid: lastUsed.Valid}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteToken removes a token by id (admin-only). Cross-user safe via
// being a primary-key delete. Also writes a RevokedToken row so the
// bearer JWT stops authenticating immediately — see DeleteUserToken.
func (d *DB) DeleteToken(ctx context.Context, id string) error {
	return d.deleteAndRevokeToken(ctx, `DELETE FROM "Token" WHERE id = $1 RETURNING "expiresAt"`,
		id, id)
}

// deleteAndRevokeToken runs a DELETE ... RETURNING "expiresAt" that
// removes exactly one Token row, then — in the same transaction —
// inserts a RevokedToken row keyed on jti so the deleted token's bearer
// JWT is rejected by the auth middleware from that point on. The jti is
// the Token row id (bound at issue time). Returns ErrNotFound when the
// DELETE matched no row. deleteArgs are the params for the DELETE query;
// jti + the row's userId key the revocation.
//
// userID may be "" for the admin delete-by-id path (RevokedToken.userId
// is informational; the middleware probes by jti). We fetch the row's
// real userId from the delete when the query returns it; callers that
// don't select it pass "" and we fall back to a lookup-free revoke.
func (d *DB) deleteAndRevokeToken(ctx context.Context, deleteQuery, jti string, deleteArgs ...any) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: delete token begin: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	var expiresAt prismaTime
	if err := tx.QueryRowContext(ctx, deleteQuery, deleteArgs...).Scan(&expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("db: delete token: %w", err)
	}

	exp := expiresAt.Time
	if exp.IsZero() {
		exp = time.Now().Add(100 * 365 * 24 * time.Hour)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "RevokedToken" ("jti", "userId", "reason", "expiresAt")
VALUES ($1, '', 'token-deleted', $2)
ON CONFLICT ("jti") DO UPDATE SET
  "reason" = excluded."reason",
  "expiresAt" = excluded."expiresAt"`,
		jti, exp.UTC(),
	); err != nil {
		return fmt.Errorf("db: revoke deleted token: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: delete token commit: %w", err)
	}
	rollback = false
	return nil
}
