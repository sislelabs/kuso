package db

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// PermissionRow is one row from the Permission table joined as part of
// a role's permission list.
type PermissionRow struct {
	ID       string
	Resource string
	Action   string
}

// FullRole bundles a role with its permissions, matching what
// /api/roles returns.
type FullRole struct {
	ID          string
	Name        string
	Description string
	Permissions []PermissionRow
}

// ListRolesWithPermissions joins roles to their permissions via the
// _PermissionToRole pivot. One query per role keeps the code simple;
// the admin pages list <50 roles in practice.
func (d *DB) ListRolesWithPermissions(ctx context.Context) ([]FullRole, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT id, name, COALESCE(description, '') FROM "Role" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list roles: %w", err)
	}
	defer rows.Close()
	var out []FullRole
	for rows.Next() {
		var r FullRole
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		perms, err := d.permissionsForRole(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Permissions = perms
	}
	return out, nil
}

func (d *DB) permissionsForRole(ctx context.Context, roleID string) ([]PermissionRow, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT p.id, p.resource, p.action
FROM "_PermissionToRole" pr
JOIN "Permission" p ON p.id = pr."A"
WHERE pr."B" = ? ORDER BY p.resource, p.action`, roleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PermissionRow
	for rows.Next() {
		var p PermissionRow
		if err := rows.Scan(&p.ID, &p.Resource, &p.Action); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PermissionInput is the shape clients post when creating/updating a
// role: {resource, action}. Description / namespace aren't supported
// from the wire because the Prisma schema only stores resource+action
// as the addressable identity.
type PermissionInput struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

// CreateRole inserts a role row and the permission rows linked to it.
// Permission rows are created in this call rather than referenced by id
// to mirror the TS Prisma behaviour (the client never preexists the
// resource/action pairs).
func (d *DB) CreateRole(ctx context.Context, id, name, description string, perms []PermissionInput) error {
	if id == "" || name == "" {
		return errors.New("db: id and name required")
	}
	now := time.Now().UTC()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
		id, name, sqlNullable(description), now, now); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: insert role: %w", err)
	}
	if err := insertRolePermissions(ctx, tx, id, perms); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpdateRole replaces a role's name + description + permissions atomically.
func (d *DB) UpdateRole(ctx context.Context, id, name, description string, perms []PermissionInput) error {
	now := time.Now().UTC()
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	res, err := tx.ExecContext(ctx, `
UPDATE "Role" SET name = ?, description = ?, "updatedAt" = ? WHERE id = ?`,
		name, sqlNullable(description), now, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: update role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	// Drop existing pivot rows + the orphan permission rows. This
	// matches Prisma's deleteMany then recreate behaviour.
	if _, err := tx.ExecContext(ctx, `DELETE FROM "_PermissionToRole" WHERE "B" = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: clear permissions pivot: %w", err)
	}
	if err := insertRolePermissions(ctx, tx, id, perms); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// DeleteRole removes a role and its permission pivot rows.
func (d *DB) DeleteRole(ctx context.Context, id string) error {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM "_PermissionToRole" WHERE "B" = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: clear pivot: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM "Role" WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: delete role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	return tx.Commit()
}

// insertRolePermissions creates a Permission row + pivot row for each
// (resource, action) pair the caller supplied. Each Permission row is
// new — Prisma schema has no uniqueness on resource+action so we
// follow the same shape.
func insertRolePermissions(ctx context.Context, tx *sql.Tx, roleID string, perms []PermissionInput) error {
	now := time.Now().UTC()
	for _, p := range perms {
		if p.Resource == "" || p.Action == "" {
			continue
		}
		permID := mustRandomID()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "Permission" (id, resource, action, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
			permID, p.Resource, p.Action, now, now); err != nil {
			return fmt.Errorf("db: insert permission: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO "_PermissionToRole" ("A", "B") VALUES (?, ?)`, permID, roleID); err != nil {
			return fmt.Errorf("db: link permission: %w", err)
		}
	}
	return nil
}

// mustRandomID returns a hex-encoded 16-byte random id. Panics on
// rand.Read failure (which would mean the system's CSPRNG is broken,
// at which point we can't safely continue anyway).
func mustRandomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Errorf("db: rand: %w", err))
	}
	return hex.EncodeToString(b[:])
}
