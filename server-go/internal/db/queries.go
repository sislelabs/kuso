package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ListUsers returns every user as a slim profile shape suitable for the
// admin user-picker UI. Password is not selected.
type UserSummary struct {
	ID        string
	Username  string
	Email     string
	FirstName sql.NullString
	LastName  sql.NullString
	IsActive  bool
	RoleName  sql.NullString
}

// ListUsers returns the slim admin-list shape.
func (d *DB) ListUsers(ctx context.Context) ([]UserSummary, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT u.id, u.username, u.email, u."firstName", u."lastName", u."isActive", r.name
FROM "User" u LEFT JOIN "Role" r ON r.id = u."roleId"
ORDER BY u.username`)
	if err != nil {
		return nil, fmt.Errorf("db: list users: %w", err)
	}
	defer rows.Close()
	var out []UserSummary
	for rows.Next() {
		var u UserSummary
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.FirstName, &u.LastName, &u.IsActive, &u.RoleName); err != nil {
			return nil, fmt.Errorf("db: scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// CountUsers returns the total user count. Used by the dashboard.
func (d *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM "User"`).Scan(&n); err != nil {
		return 0, fmt.Errorf("db: count users: %w", err)
	}
	return n, nil
}

// Role is the list shape for /api/roles.
type Role struct {
	ID          string
	Name        string
	Description sql.NullString
}

// ListRoles returns all roles ordered by name.
func (d *DB) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT id, name, description FROM "Role" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list roles: %w", err)
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		var r Role
		if err := rows.Scan(&r.ID, &r.Name, &r.Description); err != nil {
			return nil, fmt.Errorf("db: scan role: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Group is the list shape for /api/groups.
type Group struct {
	ID          string
	Name        string
	Description sql.NullString
}

// ListGroups returns all groups ordered by name.
func (d *DB) ListGroups(ctx context.Context) ([]Group, error) {
	rows, err := d.DB.QueryContext(ctx, `SELECT id, name, description FROM "UserGroup" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list groups: %w", err)
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description); err != nil {
			return nil, fmt.Errorf("db: scan group: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// AuditEntry is one row from /api/audit (newest-first list).
type AuditEntry struct {
	ID        int
	Timestamp string
	Severity  string
	Action    string
	Namespace string
	Phase     string
	App       string
	Pipeline  string
	Resource  string
	Message   string
	User      string
}

// ListAudit returns the newest `limit` audit entries.
func (d *DB) ListAudit(ctx context.Context, limit int) ([]AuditEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, timestamp, severity, action, namespace, phase, app, pipeline, resource, message, user
FROM "Audit" ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("db: list audit: %w", err)
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var a AuditEntry
		if err := rows.Scan(&a.ID, &a.Timestamp, &a.Severity, &a.Action, &a.Namespace, &a.Phase, &a.App, &a.Pipeline, &a.Resource, &a.Message, &a.User); err != nil {
			return nil, fmt.Errorf("db: scan audit: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

