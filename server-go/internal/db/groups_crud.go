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
INSERT INTO "UserGroup" (id, name, description, "createdAt", "updatedAt") VALUES ($1, $2, $3, $4, $5)`,
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
		`SELECT id, name, description FROM "UserGroup" WHERE id = $1`, id)
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

// PendingGroupName is the name of the group fresh sign-ups land in until
// an admin grants them access. Matched by name, not id, because older
// installs created it under different ids.
const PendingGroupName = "kuso-pending"

// ClearStalePending removes userID from the pending group once an admin
// has approved them — i.e. they belong to some other group or carry a
// direct admin/editor/viewer instance role. A user whose only access is
// the pending group keeps it. Returns how many memberships were removed.
func (d *DB) ClearStalePending(ctx context.Context, userID string) (int, error) {
	if userID == "" {
		return 0, errors.New("db: userID required")
	}
	return d.clearStalePending(ctx, userID)
}

// HealStalePendingMemberships is the boot-time sweep form of
// ClearStalePending: it applies the same rule to every user.
func (d *DB) HealStalePendingMemberships(ctx context.Context) (int, error) {
	return d.clearStalePending(ctx, "")
}

// clearStalePending holds the one approval predicate both entry points
// share. userID "" means every user.
func (d *DB) clearStalePending(ctx context.Context, userID string) (int, error) {
	rows, err := d.QueryContext(ctx, `
DELETE FROM "_UserToUserGroup" m
USING "UserGroup" pg
WHERE m."B" = pg.id
  AND pg.name = $1
  AND ($2 = '' OR m."A" = $2)
  AND (
    EXISTS (
      SELECT 1 FROM "_UserToUserGroup" o
      JOIN "UserGroup" og ON og.id = o."B"
      WHERE o."A" = m."A" AND og.name <> $1
    )
    OR EXISTS (
      SELECT 1 FROM "User" u
      WHERE u.id = m."A" AND u."instanceRole" IN ($3, $4, $5)
    )
  )
RETURNING m."A"`,
		PendingGroupName, userID,
		string(InstanceRoleAdmin), string(InstanceRoleEditor), string(InstanceRoleViewer))
	if err != nil {
		return 0, fmt.Errorf("db: clear stale pending: %w", err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return n, fmt.Errorf("db: scan cleared pending user: %w", err)
		}
		d.EvictUserTenancy(uid)
		n++
	}
	return n, rows.Err()
}

// UpdateGroup replaces name + description.
func (d *DB) UpdateGroup(ctx context.Context, id, name, description string) error {
	res, err := d.ExecContext(ctx, `
UPDATE "UserGroup" SET name = $1, description = $2, "updatedAt" = $3 WHERE id = $4`,
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
		`SELECT "instanceRole", "projectMemberships" FROM "UserGroup" WHERE id = $1`, groupID).
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
		`UPDATE "UserGroup" SET "instanceRole" = $1, "projectMemberships" = $2, "updatedAt" = $3 WHERE id = $4`,
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

// ListUserTenancy resolves a user's full effective tenancy for the auth
// layer (role system v2). It unions:
//
//   - the user's DIRECT instance role (User.instanceRole) + every group's
//     instance role → highest-wins.
//   - every ProjectGrant addressing the user directly OR via a group they
//     belong to → per-project effective role.
//
// Per-project effective role for one grant = its roleOverride if set,
// else the user's resolved instance role (inherited), else viewer (an
// explicit grant always confers ≥ read). Across multiple grants on the
// same project, highest-wins.
//
// The legacy UserGroup.projectMemberships JSON is no longer read.
func (d *DB) ListUserTenancy(ctx context.Context, userID string) (GroupTenancy, error) {
	a, err := d.ListUserAccess(ctx, userID)
	if err != nil {
		return GroupTenancy{}, err
	}
	return a.Tenancy(), nil
}

// UserAccessGroup is one group a user belongs to, with the instance
// role it carries.
type UserAccessGroup struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	InstanceRole InstanceRole `json:"instanceRole"`
}

// UserAccessGrant is one ProjectGrant that reaches a user, either
// directly (GroupID == "") or through a group membership.
type UserAccessGrant struct {
	Project      string      `json:"project"`
	RoleOverride ProjectRole `json:"roleOverride"`
	GroupID      string      `json:"groupId,omitempty"`
	GroupName    string      `json:"groupName,omitempty"`
}

// UserAccess is the raw material the tenancy resolver works from: every
// source of access a user has, before the highest-wins union. Tenancy()
// is the ONLY place that union happens, so the auth layer
// (ListUserTenancy) and the "what can this user see" admin view read
// the same answer.
type UserAccess struct {
	DirectInstanceRole InstanceRole      `json:"directInstanceRole"`
	Groups             []UserAccessGroup `json:"groups"`
	Grants             []UserAccessGrant `json:"grants"`
}

// ListUserAccess loads every access source for a user. An unknown user
// yields an empty UserAccess (no error), matching ListUserTenancy's
// long-standing behaviour.
func (d *DB) ListUserAccess(ctx context.Context, userID string) (UserAccess, error) {
	out := UserAccess{Groups: []UserAccessGroup{}, Grants: []UserAccessGrant{}}

	var directRole sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT "instanceRole" FROM "User" WHERE id = $1`, userID).Scan(&directRole); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return UserAccess{}, fmt.Errorf("db: user instance role: %w", err)
		}
	}
	out.DirectInstanceRole = InstanceRole(directRole.String)

	grpRows, err := d.QueryContext(ctx, `
SELECT g.id, g.name, g."instanceRole"
FROM "UserGroup" g
JOIN "_UserToUserGroup" m ON m."B" = g.id
WHERE m."A" = $1
ORDER BY g.name`, userID)
	if err != nil {
		return UserAccess{}, fmt.Errorf("db: list user groups: %w", err)
	}
	for grpRows.Next() {
		var g UserAccessGroup
		var role sql.NullString
		if err := grpRows.Scan(&g.ID, &g.Name, &role); err != nil {
			grpRows.Close()
			return UserAccess{}, err
		}
		g.InstanceRole = InstanceRole(role.String)
		out.Groups = append(out.Groups, g)
	}
	grpRows.Close()
	if err := grpRows.Err(); err != nil {
		return UserAccess{}, err
	}

	pgRows, err := d.QueryContext(ctx, `
SELECT pg.project, pg."roleOverride", pg."groupId", g.name
FROM "ProjectGrant" pg
LEFT JOIN "UserGroup" g ON g.id = pg."groupId"
WHERE pg."userId" = $1
   OR pg."groupId" IN (SELECT m."B" FROM "_UserToUserGroup" m WHERE m."A" = $2)
ORDER BY pg.project, g.name NULLS FIRST`,
		userID, userID)
	if err != nil {
		return UserAccess{}, fmt.Errorf("db: list project grants: %w", err)
	}
	defer pgRows.Close()
	for pgRows.Next() {
		var g UserAccessGrant
		var override, groupID, groupName sql.NullString
		if err := pgRows.Scan(&g.Project, &override, &groupID, &groupName); err != nil {
			return UserAccess{}, err
		}
		g.RoleOverride = ProjectRole(override.String)
		g.GroupID, g.GroupName = groupID.String, groupName.String
		out.Grants = append(out.Grants, g)
	}
	if err := pgRows.Err(); err != nil {
		return UserAccess{}, err
	}
	return out, nil
}

// InstanceRole is the highest-wins union of the direct role and every
// group's role.
func (a UserAccess) InstanceRole() InstanceRole {
	best := a.DirectInstanceRole
	for _, g := range a.Groups {
		if rankInstance(g.InstanceRole) > rankInstance(best) {
			best = g.InstanceRole
		}
	}
	return best
}

// GrantRole is the project role one grant confers: its override, else
// the user's resolved instance role (inherited), else viewer (an
// explicit grant always confers at least read). A result that is not a
// project role (a legacy direct "pending") confers nothing — Tenancy
// skips it.
func (a UserAccess) GrantRole(g UserAccessGrant) ProjectRole {
	role := g.RoleOverride
	if role == "" {
		role = ProjectRole(a.InstanceRole())
	}
	if role == "" {
		role = ProjectRoleViewer
	}
	return role
}

// Tenancy folds the sources into the effective tenancy the auth layer
// enforces: highest-wins instance role, highest-wins role per project.
func (a UserAccess) Tenancy() GroupTenancy {
	out := GroupTenancy{InstanceRole: a.InstanceRole()}
	memByProject := map[string]ProjectRole{}
	var order []string
	for _, g := range a.Grants {
		role := a.GrantRole(g)
		if rankProject(role) > rankProject(memByProject[g.Project]) {
			if _, seen := memByProject[g.Project]; !seen {
				order = append(order, g.Project)
			}
			memByProject[g.Project] = role
		}
	}
	for _, project := range order {
		out.ProjectMemberships = append(out.ProjectMemberships, ProjectMembership{Project: project, Role: memByProject[project]})
	}
	return out
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
INSERT INTO "_UserToUserGroup" ("A", "B") VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, groupID)
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
DELETE FROM "_UserToUserGroup" WHERE "A" = $1 AND "B" = $2`, userID, groupID)
	if err != nil {
		return fmt.Errorf("db: remove user %s from group %s: %w", userID, groupID, err)
	}
	d.EvictUserTenancy(userID)
	return nil
}

// GroupMember is one user belonging to a group, for the admin
// member-list view.
type GroupMember struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Email    string `json:"email"`
}

// ListGroupMembers returns the users in a group, ordered by username.
// Closes the long-standing gap where the UI could add/remove members
// but never list them (membership was a one-way join through the user
// profile). Used by GET /api/groups/{id}/members.
func (d *DB) ListGroupMembers(ctx context.Context, groupID string) ([]GroupMember, error) {
	rows, err := d.QueryContext(ctx, `
SELECT u.id, u.username, u.email
FROM "_UserToUserGroup" m
JOIN "User" u ON u.id = m."A"
WHERE m."B" = $1
ORDER BY u.username`, groupID)
	if err != nil {
		return nil, fmt.Errorf("db: list group members: %w", err)
	}
	defer rows.Close()
	out := []GroupMember{}
	for rows.Next() {
		var g GroupMember
		if err := rows.Scan(&g.ID, &g.Username, &g.Email); err != nil {
			return nil, fmt.Errorf("db: scan group member: %w", err)
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// DeleteGroup removes a group + its membership pivot rows.
func (d *DB) DeleteGroup(ctx context.Context, id string) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: begin: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM "_UserToUserGroup" WHERE "B" = $1`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("db: clear membership: %w", err)
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM "UserGroup" WHERE id = $1`, id)
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

// GroupProjectRoles returns the effective role each of a group's
// ProjectGrants confers on its members: the override, else the group's
// instance role, else viewer. Mirrors ListUserTenancy's per-grant
// resolution so access-management gates can weigh what touching the
// group would take away.
func (d *DB) GroupProjectRoles(ctx context.Context, groupID string) ([]ProjectMembership, error) {
	var instanceRole sql.NullString
	if err := d.QueryRowContext(ctx,
		`SELECT "instanceRole" FROM "UserGroup" WHERE id = $1`, groupID).Scan(&instanceRole); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("db: group instance role: %w", err)
	}
	rows, err := d.QueryContext(ctx,
		`SELECT project, "roleOverride" FROM "ProjectGrant" WHERE "groupId" = $1`, groupID)
	if err != nil {
		return nil, fmt.Errorf("db: list group project grants: %w", err)
	}
	defer rows.Close()
	var out []ProjectMembership
	for rows.Next() {
		var project string
		var override sql.NullString
		if err := rows.Scan(&project, &override); err != nil {
			return nil, fmt.Errorf("db: scan group project grant: %w", err)
		}
		role := ProjectRole(override.String)
		if role == "" {
			role = ProjectRole(instanceRole.String)
		}
		if rankProject(role) == 0 {
			role = ProjectRoleViewer
		}
		out = append(out, ProjectMembership{Project: project, Role: role})
	}
	return out, rows.Err()
}
