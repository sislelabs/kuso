package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Role system v2 storage: the ProjectGrant table + direct User instance
// role, plus the resolution that flattens them into a GroupTenancy.
//
// A principal's EFFECTIVE access is the union of:
//   - their direct User.instanceRole (new) and every group's instanceRole
//     → highest-wins instance role
//   - every ProjectGrant addressing them directly OR via a group they're
//     in → per-project effective role = override, else inherited instance
//     role, else viewer (an explicit grant always confers ≥ read)
//
// ProjectRoleFor/ProjectsAccessible in the auth package consume the
// flattened GroupTenancy.ProjectMemberships this layer produces.

const roleV2MigrationKey = "migration.roleSystemV2"

// migrateRoleSystemV2 runs the one-shot wipe-and-re-grant migration: on
// first boot after the v2 schema lands, every non-admin group loses its
// instance role + legacy project memberships, and no ProjectGrant rows
// are created. Admins (the bootstrap admin group + its members) keep
// full access. Marker-guarded via the Setting table so it runs once.
//
// Idempotent: a no-op on every boot after the first.
func (d *DB) migrateRoleSystemV2(ctx context.Context) error {
	var done string
	err := d.QueryRowContext(ctx,
		`SELECT value FROM "Setting" WHERE key = ?`, roleV2MigrationKey).Scan(&done)
	if err == nil {
		return nil // already migrated
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("db: role-v2 migration check: %w", err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: role-v2 begin: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			_ = tx.Rollback()
		}
	}()

	// Wipe-and-re-grant: every group that is NOT instanceRole='admin'
	// loses its role + legacy memberships. Admins survive untouched.
	// (User.instanceRole starts NULL for everyone; admins get access via
	// their admin group, so no per-user stamping is needed.)
	if _, err := tx.ExecContext(ctx, `
		UPDATE "UserGroup"
		   SET "instanceRole" = '', "projectMemberships" = '[]'
		 WHERE "instanceRole" IS DISTINCT FROM 'admin'`); err != nil {
		return fmt.Errorf("db: role-v2 wipe groups: %w", err)
	}

	// Mark done so this never re-wipes a re-granted instance.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO "Setting" (key, value, "updatedAt", "updatedBy")
		VALUES (?, ?, ?, ?)
		ON CONFLICT (key) DO NOTHING`,
		roleV2MigrationKey, "true", time.Now().UTC(), "role-system-v2"); err != nil {
		return fmt.Errorf("db: role-v2 mark: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("db: role-v2 commit: %w", err)
	}
	rollback = false
	d.EvictAllTenancy()
	return nil
}

// --- ProjectGrant CRUD -------------------------------------------------

// GranteeKind distinguishes the two grantee shapes a ProjectGrant holds.
type GranteeKind string

const (
	GranteeUser  GranteeKind = "user"
	GranteeGroup GranteeKind = "group"
)

// ProjectGrant is one access-list entry on a project. Exactly one of
// UserID / GroupID is set. RoleOverride is "" when the grant inherits
// the grantee's instance role.
type ProjectGrant struct {
	ID           string      `json:"id"`
	Project      string      `json:"project"`
	Kind         GranteeKind `json:"kind"`
	UserID       string      `json:"userId,omitempty"`
	GroupID      string      `json:"groupId,omitempty"`
	RoleOverride ProjectRole `json:"roleOverride,omitempty"`
	CreatedAt    time.Time   `json:"createdAt"`
}

// AddProjectGrant upserts a grant for (project, grantee). Re-granting the
// same grantee updates the override. Pass userID OR groupID (not both).
func (d *DB) AddProjectGrant(ctx context.Context, project, userID, groupID string, override ProjectRole) (string, error) {
	if project == "" {
		return "", errors.New("db: project required")
	}
	if (userID == "") == (groupID == "") {
		return "", errors.New("db: exactly one of userID / groupID required")
	}
	id := mustRandomID()
	var (
		uid = sqlNullable(userID)
		gid = sqlNullable(groupID)
		ovr = sqlNullable(string(override))
	)
	// Upsert keyed on the partial unique indexes (project,userId) /
	// (project,groupId). Two statements because the conflict target
	// differs by grantee kind.
	var conflict string
	if userID != "" {
		conflict = `("project","userId") WHERE "userId" IS NOT NULL`
	} else {
		conflict = `("project","groupId") WHERE "groupId" IS NOT NULL`
	}
	q := fmt.Sprintf(`
		INSERT INTO "ProjectGrant" (id, project, "userId", "groupId", "roleOverride", "createdAt")
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT %s DO UPDATE SET "roleOverride" = EXCLUDED."roleOverride"
		RETURNING id`, conflict)
	row := d.QueryRowContext(ctx, q, id, project, uid, gid, ovr, time.Now().UTC())
	var outID string
	if err := row.Scan(&outID); err != nil {
		return "", fmt.Errorf("db: add project grant: %w", err)
	}
	d.evictGranteeTenancy(ctx, userID, groupID)
	return outID, nil
}

// RemoveProjectGrant deletes a grant by id.
func (d *DB) RemoveProjectGrant(ctx context.Context, id string) error {
	// Capture grantee for cache eviction before deleting.
	var uid, gid sql.NullString
	_ = d.QueryRowContext(ctx,
		`SELECT "userId", "groupId" FROM "ProjectGrant" WHERE id = ?`, id).Scan(&uid, &gid)
	res, err := d.ExecContext(ctx, `DELETE FROM "ProjectGrant" WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: remove project grant: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	d.evictGranteeTenancy(ctx, uid.String, gid.String)
	return nil
}

// ListProjectGrants returns every grant on a project (users + groups).
func (d *DB) ListProjectGrants(ctx context.Context, project string) ([]ProjectGrant, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT id, project, "userId", "groupId", "roleOverride", "createdAt"
		  FROM "ProjectGrant" WHERE project = ? ORDER BY "createdAt"`, project)
	if err != nil {
		return nil, fmt.Errorf("db: list project grants: %w", err)
	}
	defer rows.Close()
	var out []ProjectGrant
	for rows.Next() {
		var (
			g            ProjectGrant
			uid, gid, ov sql.NullString
		)
		if err := rows.Scan(&g.ID, &g.Project, &uid, &gid, &ov, &g.CreatedAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			g.Kind, g.UserID = GranteeUser, uid.String
		} else {
			g.Kind, g.GroupID = GranteeGroup, gid.String
		}
		g.RoleOverride = ProjectRole(ov.String)
		out = append(out, g)
	}
	return out, rows.Err()
}

// SetUserInstanceRole sets (or clears, with "") a user's direct instance
// role. Evicts the user's tenancy cache.
func (d *DB) SetUserInstanceRole(ctx context.Context, userID string, role InstanceRole) error {
	res, err := d.ExecContext(ctx,
		`UPDATE "User" SET "instanceRole" = ?, "updatedAt" = ? WHERE id = ?`,
		sqlNullable(string(role)), prismaNow(), userID)
	if err != nil {
		return fmt.Errorf("db: set user instance role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	d.EvictUserTenancy(userID)
	return nil
}

// evictGranteeTenancy clears the tenancy cache for whichever grantee a
// grant change touched. A group change affects every member, so we evict
// the whole cache for groups (mirrors SetGroupTenancy).
func (d *DB) evictGranteeTenancy(ctx context.Context, userID, groupID string) {
	if userID != "" {
		d.EvictUserTenancy(userID)
		return
	}
	if groupID != "" {
		d.EvictAllTenancy()
	}
}
