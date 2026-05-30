package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// CreateGroup inserts a new UserGroup row.
func (d *DB) CreateGroup(ctx context.Context, id, name, description string) error {
	if id == "" || name == "" {
		return errors.New("db: id and name required")
	}
	now := prismaNow()
	_, err := d.ExecContext(ctx, `
INSERT INTO "UserGroup" (id, name, description, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
		id, name, sqlNullable(description), now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create group: %w", err)
	}
	return nil
}

// GetGroup returns a Group by ID, or ErrNotFound. Cheap lookup used
// by invite validation to confirm a configured groupId actually
// exists before we mint the link. Reuses the Group struct defined
// in queries.go.
func (d *DB) GetGroup(ctx context.Context, id string) (*Group, error) {
	row := d.QueryRowContext(ctx,
		`SELECT id, name, description FROM "UserGroup" WHERE id = ?`, id)
	var g Group
	if err := row.Scan(&g.ID, &g.Name, &g.Description); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: get group: %w", err)
	}
	return &g, nil
}

// AddUserToPendingGroup ensures a "kuso-pending" group exists with
// instanceRole=pending and adds the user to it. Used by signup paths
// that don't have a more specific group to attach to (e.g. an invite
// minted without a groupId, or a fresh OAuth login). Mirrors the
// inline helper in oauth.go.
func (d *DB) AddUserToPendingGroup(ctx context.Context, userID string) error {
	const gid = "grp-pending"
	_ = d.CreateGroup(ctx, gid, "kuso-pending", "users awaiting admin approval")
	if err := d.SetGroupTenancy(ctx, gid, GroupTenancy{InstanceRole: InstanceRolePending}); err != nil {
		return err
	}
	return d.AddUserToGroup(ctx, userID, gid)
}

// UpdateGroup replaces name + description.
func (d *DB) UpdateGroup(ctx context.Context, id, name, description string) error {
	res, err := d.ExecContext(ctx, `
UPDATE "UserGroup" SET name = ?, description = ?, "updatedAt" = ? WHERE id = ?`,
		name, sqlNullable(description), prismaNow(), id,
	)
	if err != nil {
		return fmt.Errorf("db: update group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// InstanceRole is the cluster-wide role a principal (user or group)
// carries — the *access level* they act with. Role system v2 collapsed
// the old admin/member/viewer/billing/pending set to three:
//
//	admin  — full access to everything, all projects, all instance settings.
//	editor — full project read+write incl. writing env vars, but no env
//	         value read, no pod shell, no SQL console; no instance-admin.
//	viewer — read-only.
//
// The instance role grants no project visibility on its own (except
// admin, who sees all); visibility comes from ProjectGrant rows. Empty
// string ("") means "no instance role" — a logged-in principal with no
// role and no grants sees nothing (the old "pending" state).
type InstanceRole string

const (
	InstanceRoleAdmin  InstanceRole = "admin"
	InstanceRoleEditor InstanceRole = "editor"
	InstanceRoleViewer InstanceRole = "viewer"
	// InstanceRolePending is retained ONLY so legacy rows / invite flows
	// that still write "pending" resolve to "no access" rather than an
	// unknown role. New code should use "" (empty) for no-access.
	InstanceRolePending InstanceRole = "pending"
)

// ProjectRole is the effective role a principal has ON a specific
// project. Same three-role vocabulary as InstanceRole — admin > editor
// > viewer. A ProjectGrant may carry an explicit override; absent an
// override the grant inherits the principal's instance role.
type ProjectRole string

const (
	ProjectRoleAdmin  ProjectRole = "admin"
	ProjectRoleEditor ProjectRole = "editor"
	ProjectRoleViewer ProjectRole = "viewer"
)

// ProjectMembership is one entry in UserGroup.projectMemberships
// (stored as JSON-encoded slice).
type ProjectMembership struct {
	Project string      `json:"project"`
	Role    ProjectRole `json:"role"`
}

// GroupTenancy bundles the fields added by the v0.5 migration so
// callers don't have to scan five strings.
type GroupTenancy struct {
	InstanceRole       InstanceRole        `json:"instanceRole"`
	ProjectMemberships []ProjectMembership `json:"projectMemberships"`
}

// GetGroupTenancy reads the instanceRole + projectMemberships JSON
// for one group. Returns ErrNotFound when the group is gone.
func (d *DB) GetGroupTenancy(ctx context.Context, groupID string) (*GroupTenancy, error) {
	var role string
	var memJSON string
	err := d.QueryRowContext(ctx,
		`SELECT "instanceRole", "projectMemberships" FROM "UserGroup" WHERE id = ?`, groupID).
		Scan(&role, &memJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("db: get group tenancy: %w", err)
	}
	var mem []ProjectMembership
	if memJSON != "" {
		if err := json.Unmarshal([]byte(memJSON), &mem); err != nil {
			return nil, fmt.Errorf("db: decode projectMemberships: %w", err)
		}
	}
	return &GroupTenancy{InstanceRole: InstanceRole(role), ProjectMemberships: mem}, nil
}

// SetGroupTenancy replaces both tenancy columns atomically. Pass
// nil/empty to clear projectMemberships.
func (d *DB) SetGroupTenancy(ctx context.Context, groupID string, t GroupTenancy) error {
	// Empty instance role = "no access" in v2 (was the removed "member"
	// default). A group with no role and no grants confers nothing; an
	// admin must explicitly set viewer/editor/admin.
	memBytes, err := json.Marshal(t.ProjectMemberships)
	if err != nil {
		return fmt.Errorf("db: encode projectMemberships: %w", err)
	}
	res, err := d.ExecContext(ctx,
		`UPDATE "UserGroup" SET "instanceRole" = ?, "projectMemberships" = ?, "updatedAt" = ? WHERE id = ?`,
		string(t.InstanceRole), string(memBytes), prismaNow(), groupID)
	if err != nil {
		return fmt.Errorf("db: set group tenancy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Group's project-membership shape changed — every member's
	// cached tenancy is now stale. The matching InvalidateUsersByGroup
	// call site already runs after this returns and would catch it,
	// but evicting up-front avoids serving one stale request between
	// the two writes.
	d.EvictAllTenancy()
	return nil
}

// ListUserTenancy walks every group a user belongs to and returns
// the union of project memberships + the highest instance role.
// Used by the auth flow to build the JWT permission claim.
//
// Highest-wins on instance role: admin > billing > viewer > member > pending.
// On project role: owner > deployer > viewer (per-project).
func (d *DB) ListUserTenancy(ctx context.Context, userID string) (GroupTenancy, error) {
	rows, err := d.QueryContext(ctx, `
SELECT g."instanceRole", g."projectMemberships"
FROM "UserGroup" g
JOIN "_UserToUserGroup" m ON m."B" = g.id
WHERE m."A" = ?`, userID)
	if err != nil {
		return GroupTenancy{}, fmt.Errorf("db: list user tenancy: %w", err)
	}
	defer rows.Close()

	bestInstance := InstanceRolePending
	memByProject := map[string]ProjectRole{}
	for rows.Next() {
		var role, memJSON string
		if err := rows.Scan(&role, &memJSON); err != nil {
			return GroupTenancy{}, err
		}
		if rankInstance(InstanceRole(role)) > rankInstance(bestInstance) {
			bestInstance = InstanceRole(role)
		}
		if memJSON != "" {
			var mems []ProjectMembership
			if err := json.Unmarshal([]byte(memJSON), &mems); err != nil {
				continue
			}
			for _, m := range mems {
				if rankProject(m.Role) > rankProject(memByProject[m.Project]) {
					memByProject[m.Project] = m.Role
				}
			}
		}
	}
	out := GroupTenancy{InstanceRole: bestInstance}
	for project, role := range memByProject {
		out.ProjectMemberships = append(out.ProjectMemberships, ProjectMembership{Project: project, Role: role})
	}
	return out, nil
}

// rankInstance orders instance roles for highest-wins union across a
// principal's groups + direct role. admin > editor > viewer > none.
func rankInstance(r InstanceRole) int {
	switch r {
	case InstanceRoleAdmin:
		return 4
	case InstanceRoleEditor:
		return 3
	case InstanceRoleViewer:
		return 2
	case InstanceRolePending, "":
		return 1
	}
	return 0
}

// rankProject orders effective project roles. admin > editor > viewer.
func rankProject(r ProjectRole) int {
	switch r {
	case ProjectRoleAdmin:
		return 3
	case ProjectRoleEditor:
		return 2
	case ProjectRoleViewer:
		return 1
	}
	return 0
}

// AddUserToGroup is the idempotent membership upsert. Used by the
// OAuth bootstrap path to drop a fresh user into the admin or
// pending group without caring whether the row already exists.
func (d *DB) AddUserToGroup(ctx context.Context, userID, groupID string) error {
	_, err := d.ExecContext(ctx, `
INSERT OR IGNORE INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`, userID, groupID)
	if err != nil {
		return fmt.Errorf("db: add user %s to group %s: %w", userID, groupID, err)
	}
	d.EvictUserTenancy(userID)
	return nil
}

// RemoveUserFromGroup is the inverse — used when an admin clicks
// "remove from group" in the UI editor.
func (d *DB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	_, err := d.ExecContext(ctx, `
DELETE FROM "_UserToUserGroup" WHERE "A" = ? AND "B" = ?`, userID, groupID)
	if err != nil {
		return fmt.Errorf("db: remove user %s from group %s: %w", userID, groupID, err)
	}
	d.EvictUserTenancy(userID)
	return nil
}

// DeleteGroup removes a group + its membership pivot rows.
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM "_UserToUserGroup" WHERE "B" = ?`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: clear membership: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM "UserGroup" WHERE id = ?`, id)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: delete group: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_ = tx.Rollback()
		return ErrNotFound
	}
	return tx.Commit()
}
