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
	_, err := d.DB.ExecContext(ctx, `
INSERT INTO "UserGroup" (id, name, description, "createdAt", "updatedAt") VALUES (?, ?, ?, ?, ?)`,
		id, name, sqlNullable(description), now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create group: %w", err)
	}
	return nil
}

// UpdateGroup replaces name + description.
func (d *DB) UpdateGroup(ctx context.Context, id, name, description string) error {
	res, err := d.DB.ExecContext(ctx, `
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

// InstanceRole is the cluster-wide role a Group carries. Distinct
// from project_role (per-project, in the JSON column) because some
// permissions don't fit a project — billing, settings, audit, etc.
type InstanceRole string

const (
	InstanceRoleAdmin   InstanceRole = "admin"
	InstanceRoleMember  InstanceRole = "member"
	InstanceRoleViewer  InstanceRole = "viewer"
	InstanceRoleBilling InstanceRole = "billing"
	InstanceRolePending InstanceRole = "pending"
)

// ProjectRole is per-project. owner > deployer > viewer.
type ProjectRole string

const (
	ProjectRoleOwner    ProjectRole = "owner"
	ProjectRoleDeployer ProjectRole = "deployer"
	ProjectRoleViewer   ProjectRole = "viewer"
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
	err := d.DB.QueryRowContext(ctx,
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
	if t.InstanceRole == "" {
		t.InstanceRole = InstanceRoleMember
	}
	memBytes, err := json.Marshal(t.ProjectMemberships)
	if err != nil {
		return fmt.Errorf("db: encode projectMemberships: %w", err)
	}
	res, err := d.DB.ExecContext(ctx,
		`UPDATE "UserGroup" SET "instanceRole" = ?, "projectMemberships" = ?, "updatedAt" = ? WHERE id = ?`,
		string(t.InstanceRole), string(memBytes), prismaNow(), groupID)
	if err != nil {
		return fmt.Errorf("db: set group tenancy: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUserTenancy walks every group a user belongs to and returns
// the union of project memberships + the highest instance role.
// Used by the auth flow to build the JWT permission claim.
//
// Highest-wins on instance role: admin > billing > viewer > member > pending.
// On project role: owner > deployer > viewer (per-project).
func (d *DB) ListUserTenancy(ctx context.Context, userID string) (GroupTenancy, error) {
	rows, err := d.DB.QueryContext(ctx, `
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

func rankInstance(r InstanceRole) int {
	switch r {
	case InstanceRoleAdmin:
		return 5
	case InstanceRoleBilling:
		return 4
	case InstanceRoleViewer:
		return 3
	case InstanceRoleMember:
		return 2
	case InstanceRolePending, "":
		return 1
	}
	return 0
}

func rankProject(r ProjectRole) int {
	switch r {
	case ProjectRoleOwner:
		return 3
	case ProjectRoleDeployer:
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
	_, err := d.DB.ExecContext(ctx, `
INSERT OR IGNORE INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`, userID, groupID)
	if err != nil {
		return fmt.Errorf("db: add user %s to group %s: %w", userID, groupID, err)
	}
	return nil
}

// RemoveUserFromGroup is the inverse — used when an admin clicks
// "remove from group" in the UI editor.
func (d *DB) RemoveUserFromGroup(ctx context.Context, userID, groupID string) error {
	_, err := d.DB.ExecContext(ctx, `
DELETE FROM "_UserToUserGroup" WHERE "A" = ? AND "B" = ?`, userID, groupID)
	if err != nil {
		return fmt.Errorf("db: remove user %s from group %s: %w", userID, groupID, err)
	}
	return nil
}

// DeleteGroup removes a group + its membership pivot rows.
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	tx, err := d.DB.BeginTx(ctx, nil)
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
