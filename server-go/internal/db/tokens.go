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
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "Token" (id, name, "userId", "expiresAt", "isActive", role, groups, "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, '', '', ?, ?)`,
		t.ID, t.Name, t.UserID, t.ExpiresAt, t.IsActive, t.CreatedAt, now,
	)
	if err != nil {
		return fmt.Errorf("db: create token: %w", err)
	}
	return nil
}

// ListTokensForUser returns the user's tokens, newest first.
func (d *DB) ListTokensForUser(ctx context.Context, userID string) ([]Token, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, name, "userId", "expiresAt", "isActive", "lastUsed", "lastIp", "createdAt"
FROM "Token" WHERE "userId" = ? ORDER BY "createdAt" DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("db: list tokens: %w", err)
	}
	defer rows.Close()
	var out []Token
	for rows.Next() {
		var t Token
		if err := rows.Scan(&t.ID, &t.Name, &t.UserID, &t.ExpiresAt, &t.IsActive, &t.LastUsed, &t.LastIP, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scan token: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteUserToken removes a single token belonging to userID. Refusing
// to delete cross-user tokens is the call site's job — for the /my/:id
// route it's enforced by the WHERE clause itself.
func (d *DB) DeleteUserToken(ctx context.Context, userID, tokenID string) error {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "Token" WHERE id = ? AND "userId" = ?`, tokenID, userID)
	if err != nil {
		return fmt.Errorf("db: delete token: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
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
	rows, err := d.DB.QueryContext(ctx, `
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
		if err := rows.Scan(&a.ID, &a.Name, &a.UserID, &a.Username, &a.Email, &a.ExpiresAt, &a.IsActive, &a.LastUsed, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DeleteToken removes a token by id (admin-only). Cross-user safe via
// being a primary-key delete.
func (d *DB) DeleteToken(ctx context.Context, id string) error {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "Token" WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete token: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
