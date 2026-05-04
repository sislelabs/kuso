package db

import (
	"context"
	"database/sql"
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

// EnsureAdminGroup makes sure there's always an admin escape hatch
// in the cluster. Called from main on every boot:
//
//   - If no group has instanceRole=admin, create "kuso-admins" with
//     that role.
//   - If the admin group has zero members but a local "admin"-style
//     user exists, attach that user. This covers the v0.4 → v0.5
//     migration: legacy clusters had a password seed admin and no
//     groups; without this they'd boot v0.5 and have no path to
//     administer.
//
// Idempotent. Safe to call on every boot.
func (d *DB) EnsureAdminGroup(ctx context.Context, seedUsername string) error {
	// 1. Find or create the admin group.
	var groupID string
	row := d.DB.QueryRowContext(ctx,
		`SELECT id FROM "UserGroup" WHERE "instanceRole" = 'admin' LIMIT 1`)
	err := row.Scan(&groupID)
	if err == sql.ErrNoRows {
		groupID = "grp-bootstrap-admins"
		now := prismaNow()
		if _, err := d.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO "UserGroup" (id, name, description, "instanceRole", "projectMemberships", "createdAt", "updatedAt")
VALUES (?, 'kuso-admins', 'instance administrators (auto-created)', 'admin', '[]', ?, ?)`,
			groupID, now, now); err != nil {
			return fmt.Errorf("db: ensure admin group: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("db: scan admin group: %w", err)
	}

	// 2. If the group is empty AND a seed user exists, attach them.
	var memberCount int
	if err := d.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM "_UserToUserGroup" WHERE "B" = ?`, groupID).Scan(&memberCount); err != nil {
		return fmt.Errorf("db: count admin group members: %w", err)
	}
	if memberCount == 0 && seedUsername != "" {
		var seedID string
		err := d.DB.QueryRowContext(ctx,
			`SELECT id FROM "User" WHERE username = ?`, seedUsername).Scan(&seedID)
		if err == nil {
			if _, err := d.DB.ExecContext(ctx,
				`INSERT OR IGNORE INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`,
				seedID, groupID); err != nil {
				return fmt.Errorf("db: attach seed admin: %w", err)
			}
		}
		// If no seed user — fine. The first OAuth login will land
		// in this group via the disaster-recovery promotion in the
		// auth handler.
	}
	return nil
}

// PromoteUserToAdminIfNoAdmin atomically checks "does the cluster
// have any non-seed admin-group member?" and, if not, attaches
// userID to the admin group. Used by the login flow as a
// disaster-recovery path AND as the first-real-human onboarding:
// the install seeds a local admin account for password recovery,
// but the first person who actually OAuth-logs-in should also be
// admin — they're the operator. Subsequent OAuth users land in
// pending until granted explicitly.
//
// "Non-seed" means: any admin whose provider is NOT 'local', OR
// whose email is set AND not the synthetic seed email
// ($KUSO_ADMIN_EMAIL). Once a real human is admin, this returns
// false and pending-onboarding takes over.
//
// Returns true when promotion happened, false when a real admin
// already exists.
func (d *DB) PromoteUserToAdminIfNoAdmin(ctx context.Context, userID string) (bool, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("db: promote: begin: %w", err)
	}
	defer tx.Rollback()

	// Count admins that look like real humans. A seed local admin
	// (provider='local' AND username='admin') is excluded so the
	// first OAuth login still triggers promotion. Once any non-seed
	// admin exists — OAuth or local — promotion stops.
	var n int
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM "_UserToUserGroup" m
JOIN "UserGroup" g ON g.id = m."B"
JOIN "User" u ON u.id = m."A"
WHERE g."instanceRole" = 'admin'
  AND NOT (u.provider = 'local' AND u.username = 'admin')`).Scan(&n); err != nil {
		return false, fmt.Errorf("db: promote: count admins: %w", err)
	}
	if n > 0 {
		return false, nil
	}
	// Find or create the admin group inside the transaction.
	var groupID string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM "UserGroup" WHERE "instanceRole" = 'admin' LIMIT 1`).Scan(&groupID); err == sql.ErrNoRows {
		groupID = "grp-bootstrap-admins"
		now := prismaNow()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "UserGroup" (id, name, description, "instanceRole", "projectMemberships", "createdAt", "updatedAt")
VALUES (?, 'kuso-admins', 'instance administrators (auto-created on first login)', 'admin', '[]', ?, ?)`,
			groupID, now, now); err != nil {
			return false, fmt.Errorf("db: promote: create group: %w", err)
		}
	} else if err != nil {
		return false, fmt.Errorf("db: promote: find group: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`,
		userID, groupID); err != nil {
		return false, fmt.Errorf("db: promote: attach: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("db: promote: commit: %w", err)
	}
	return true, nil
}

// PromoteUsernameToAdmin is the env-flag escape hatch (see
// main.go's KUSO_PROMOTE_USER). Resolves the username to a User row
// and attaches it to the admin group, creating the group if it's
// missing. Also removes the user from any pending group so they
// don't get re-routed to /awaiting-access on next login.
//
// No-op when the user already belongs to the admin group.
func (d *DB) PromoteUsernameToAdmin(ctx context.Context, username string) error {
	if username == "" {
		return errors.New("db: promote: username required")
	}
	var userID string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT id FROM "User" WHERE username = ?`, username).Scan(&userID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: promote: user %q not found", username)
		}
		return fmt.Errorf("db: promote: lookup: %w", err)
	}

	// Resolve / create admin group.
	var adminGroupID string
	if err := d.DB.QueryRowContext(ctx,
		`SELECT id FROM "UserGroup" WHERE "instanceRole" = 'admin' LIMIT 1`).Scan(&adminGroupID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("db: promote: find admin group: %w", err)
		}
		adminGroupID = "grp-bootstrap-admins"
		now := prismaNow()
		if _, err := d.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO "UserGroup" (id, name, description, "instanceRole", "projectMemberships", "createdAt", "updatedAt")
VALUES (?, 'kuso-admins', 'instance administrators (auto-created)', 'admin', '[]', ?, ?)`,
			adminGroupID, now, now); err != nil {
			return fmt.Errorf("db: promote: create admin group: %w", err)
		}
	}
	if _, err := d.DB.ExecContext(ctx,
		`INSERT OR IGNORE INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`,
		userID, adminGroupID); err != nil {
		return fmt.Errorf("db: promote: attach to admin: %w", err)
	}
	// Remove from pending — otherwise the user's tenancy resolver
	// still sees a pending row in their union and the perms compute
	// to "admin" anyway, but the awaiting-access redirect logic
	// could confuse them. Belt-and-suspenders: drop the pending row.
	if _, err := d.DB.ExecContext(ctx, `
DELETE FROM "_UserToUserGroup"
WHERE "A" = ?
AND "B" IN (SELECT id FROM "UserGroup" WHERE "instanceRole" = 'pending')`,
		userID); err != nil {
		return fmt.Errorf("db: promote: clear pending: %w", err)
	}
	return nil
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
