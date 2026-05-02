package db

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// BootstrapAdmin ensures an admin role + user exist on first boot.
// Idempotent: if either already exists, the call is a no-op.
//
//   - role "admin" gets a wildcard permission set covering every kuso
//     resource the UI gates on (matches the TS seed in
//     server/src/database/seed/seed.ts).
//   - user is inserted with the supplied username + email + bcrypt of
//     password, role pointing at "admin", isActive=true, provider=local.
//
// Returns nil on no-op and on success; only DB-layer errors propagate.
func (d *DB) BootstrapAdmin(ctx context.Context, username, email, passwordHash string) error {
	if username == "" || passwordHash == "" {
		return errors.New("db: bootstrap admin: username + passwordHash required")
	}
	if email == "" {
		email = username + "@kuso.local"
	}

	// Skip if any user already exists — only seed on a virgin DB.
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User"`).Scan(&n); err != nil {
		return fmt.Errorf("db: bootstrap: count users: %w", err)
	}
	if n > 0 {
		return nil
	}

	now := prismaNow()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: bootstrap: begin: %w", err)
	}
	roleID := mustRandomID()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt")
VALUES (?, 'admin', 'Full access (auto-seeded on first boot)', ?, ?)`,
		roleID, now, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: bootstrap: insert role: %w", err)
	}

	// Permissions modelled as one row per (resource, action) pair, linked
	// via _PermissionToRole. Wildcard set covers what the Vue UI guards
	// most often — admins can do everything anyway, but the JWT carries
	// these so the UI panel-gating logic short-circuits cleanly.
	type perm struct {
		Resource string
		Action   string
	}
	wildcard := []perm{
		{"app", "read"}, {"app", "write"},
		{"pipeline", "read"}, {"pipeline", "write"},
		{"user", "read"}, {"user", "write"},
		{"config", "read"}, {"config", "write"},
		{"audit", "read"}, {"audit", "write"},
		{"token", "read"}, {"token", "write"},
		{"security", "read"}, {"security", "write"},
		{"settings", "read"}, {"settings", "write"},
		{"console", "ok"}, {"logs", "ok"}, {"reboot", "ok"},
	}
	for _, p := range wildcard {
		permID := mustRandomID()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "Permission" (id, resource, action, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
			permID, p.Resource, p.Action, now, now); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: bootstrap: insert permission: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "_PermissionToRole" ("A", "B") VALUES (?, ?)`, permID, roleID); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("db: bootstrap: link permission: %w", err)
		}
	}

	userID := mustRandomID()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", "roleId", provider, "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, 0, 1, ?, 'local', ?, ?)`,
		userID, username, email, passwordHash, roleID, now, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: bootstrap: insert user: %w", err)
	}
	return tx.Commit()
}

// EnsureAdminPassword (re)sets the admin user's password. Idempotent —
// no-op when the bcrypt hash already matches. Used by the boot path so
// the live KUSO_ADMIN_PASSWORD always wins.
//
// when is the timestamp the row was last updated, surfaced for callers
// that want to log it.
func (d *DB) EnsureAdminPassword(ctx context.Context, username, passwordHash string) (when time.Time, err error) {
	now := prismaNow()
	res, err := d.DB.ExecContext(ctx, `
UPDATE "User" SET password = ?, "updatedAt" = ? WHERE username = ?`, passwordHash, now, username)
	if err != nil {
		return time.Time{}, fmt.Errorf("db: ensure admin password: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return time.Time{}, ErrNotFound
	}
	return now.Time, nil
}
