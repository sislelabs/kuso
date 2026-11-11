package db

import (
	"context"
	"errors"
	"testing"
)

// seedUser inserts a minimal user row for grant tests.
func seedUser(t *testing.T, d *DB, id string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ($1, $2, $3, 'h', false, true, 'local', NOW(), NOW())`, id, id, id+"@x"); err != nil {
		t.Fatalf("seed user %s: %v", id, err)
	}
}

func seedGroupWithRole(t *testing.T, d *DB, id string, role InstanceRole) {
	t.Helper()
	if err := d.CreateGroup(context.Background(), id, id, ""); err != nil {
		t.Fatalf("create group %s: %v", id, err)
	}
	if err := d.SetGroupTenancy(context.Background(), id, GroupTenancy{InstanceRole: role}); err != nil {
		t.Fatalf("set group tenancy %s: %v", id, err)
	}
}

// projectRoleOn returns the resolved effective role for a user on a
// project, or "" if invisible.
func projectRoleOn(tenancy GroupTenancy, project string) ProjectRole {
	for _, m := range tenancy.ProjectMemberships {
		if m.Project == project {
			return m.Role
		}
	}
	return ""
}

// TestProjectGrant_ResolvesRole covers how a user's effective role on a
// project is resolved from direct grants, group grants and instance role.
func TestProjectGrant_ResolvesRole(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, ctx context.Context, d *DB)
		want  ProjectRole
	}{
		// A direct user grant with an explicit override resolves to that
		// override regardless of instance role.
		{"DirectUser_Override", func(t *testing.T, ctx context.Context, d *DB) {
			mustGrant(t, ctx, d, "p1", "u1", "", ProjectRoleEditor)
		}, ProjectRoleEditor},
		// A grant with NO override inherits the user's instance role.
		{"InheritsInstanceRole", func(t *testing.T, ctx context.Context, d *DB) {
			if err := d.SetUserInstanceRole(ctx, "u1", InstanceRoleEditor); err != nil {
				t.Fatalf("set user role: %v", err)
			}
			mustGrant(t, ctx, d, "p1", "u1", "", "") // no override
		}, ProjectRoleEditor},
		// A grant with no override on a user with no instance role still
		// confers viewer.
		{"ViewerFloor", func(t *testing.T, ctx context.Context, d *DB) {
			mustGrant(t, ctx, d, "p1", "u1", "", "")
		}, ProjectRoleViewer},
		// A group grant reaches every member.
		{"ViaGroup", func(t *testing.T, ctx context.Context, d *DB) {
			seedGroupWithRole(t, d, "g1", InstanceRoleViewer)
			if err := d.AddUserToGroup(ctx, "u1", "g1"); err != nil {
				t.Fatalf("add to group: %v", err)
			}
			mustGrant(t, ctx, d, "p1", "", "g1", ProjectRoleEditor)
		}, ProjectRoleEditor},
		// Direct viewer grant + group editor grant on the same project ->
		// editor.
		{"HighestWins", func(t *testing.T, ctx context.Context, d *DB) {
			seedGroupWithRole(t, d, "g1", InstanceRoleViewer)
			_ = d.AddUserToGroup(ctx, "u1", "g1")
			mustGrant(t, ctx, d, "p1", "u1", "", ProjectRoleViewer)
			mustGrant(t, ctx, d, "p1", "", "g1", ProjectRoleEditor)
		}, ProjectRoleEditor},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := openTestDB(t)
			ctx := context.Background()
			seedUser(t, d, "u1")
			tt.setup(t, ctx, d)
			ten, err := d.ListUserTenancy(ctx, "u1")
			if err != nil {
				t.Fatalf("tenancy: %v", err)
			}
			if got := projectRoleOn(ten, "p1"); got != tt.want {
				t.Errorf("p1 role = %q, want %q", got, tt.want)
			}
		})
	}
}

func mustGrant(t *testing.T, ctx context.Context, d *DB, project, userID, groupID string, override ProjectRole) {
	t.Helper()
	if _, err := d.AddProjectGrant(ctx, project, userID, groupID, override); err != nil {
		t.Fatalf("add grant: %v", err)
	}
}

// TestProjectGrant_NoGrant_Invisible: a user with an instance role but no
// grant sees no projects.
func TestProjectGrant_NoGrant_Invisible(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "u1")
	_ = d.SetUserInstanceRole(ctx, "u1", InstanceRoleEditor)
	ten, _ := d.ListUserTenancy(ctx, "u1")
	if len(ten.ProjectMemberships) != 0 {
		t.Errorf("expected no visible projects, got %v", ten.ProjectMemberships)
	}
	if ten.InstanceRole != InstanceRoleEditor {
		t.Errorf("instance role = %q, want editor", ten.InstanceRole)
	}
}

// TestProjectGrant_Upsert: re-granting the same (project,user) updates the
// override rather than creating a duplicate.
func TestProjectGrant_Upsert(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "u1")
	if _, err := d.AddProjectGrant(ctx, "p1", "u1", "", ProjectRoleViewer); err != nil {
		t.Fatalf("grant 1: %v", err)
	}
	if _, err := d.AddProjectGrant(ctx, "p1", "u1", "", ProjectRoleEditor); err != nil {
		t.Fatalf("grant 2 (upsert): %v", err)
	}
	grants, err := d.ListProjectGrants(ctx, "p1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(grants) != 1 {
		t.Fatalf("expected 1 grant after upsert, got %d", len(grants))
	}
	if grants[0].RoleOverride != ProjectRoleEditor {
		t.Errorf("override = %q, want editor", grants[0].RoleOverride)
	}
}

// TestProjectGrant_Remove: removing a grant makes the project invisible.
func TestProjectGrant_Remove(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "u1")
	id, err := d.AddProjectGrant(ctx, "p1", "u1", "", ProjectRoleEditor)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := d.RemoveProjectGrant(ctx, "p1", id); err != nil {
		t.Fatalf("remove: %v", err)
	}
	ten, _ := d.ListUserTenancy(ctx, "u1")
	if projectRoleOn(ten, "p1") != "" {
		t.Error("project should be invisible after grant removal")
	}
}

// TestProjectGrant_RemoveScopedToProject: a grant id from another project
// must not be deletable through a different project's URL.
func TestProjectGrant_RemoveScopedToProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "u1")
	id, err := d.AddProjectGrant(ctx, "p1", "u1", "", ProjectRoleEditor)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := d.RemoveProjectGrant(ctx, "p2", id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove via other project: err = %v, want ErrNotFound", err)
	}
	ten, _ := d.ListUserTenancy(ctx, "u1")
	if projectRoleOn(ten, "p1") != ProjectRoleEditor {
		t.Error("p1 grant must survive a removal attempted through p2")
	}
}

// TestRoleV2Migration_WipesNonAdmins: the one-shot migration clears
// non-admin group roles and leaves admins intact.
func TestRoleV2Migration_WipesNonAdmins(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	// openTestDB already ran the migration once (marker is set). Reset the
	// marker so we can drive the migration against seeded legacy data.
	if _, err := d.ExecContext(ctx, `DELETE FROM "Setting" WHERE key = $1`, roleV2MigrationKey); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	seedGroupWithRole(t, d, "admins", InstanceRoleAdmin)
	seedGroupWithRole(t, d, "team", InstanceRoleEditor)
	// Give the legacy team group a stale projectMemberships blob directly.
	if _, err := d.ExecContext(ctx,
		`UPDATE "UserGroup" SET "projectMemberships" = '[{"project":"p1","role":"editor"}]' WHERE id = 'team'`); err != nil {
		t.Fatalf("seed legacy memberships: %v", err)
	}

	if err := d.migrateRoleSystemV2(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var adminRole, teamRole, teamMem string
	if err := d.QueryRowContext(ctx, `SELECT "instanceRole" FROM "UserGroup" WHERE id='admins'`).Scan(&adminRole); err != nil {
		t.Fatal(err)
	}
	if err := d.QueryRowContext(ctx, `SELECT "instanceRole", "projectMemberships" FROM "UserGroup" WHERE id='team'`).Scan(&teamRole, &teamMem); err != nil {
		t.Fatal(err)
	}
	if adminRole != "admin" {
		t.Errorf("admin group role = %q, want admin (must survive)", adminRole)
	}
	if teamRole != "" {
		t.Errorf("non-admin group role = %q, want cleared", teamRole)
	}
	if teamMem != "[]" {
		t.Errorf("non-admin legacy memberships = %q, want cleared", teamMem)
	}

	// Idempotent: second run is a no-op (marker set).
	if err := d.migrateRoleSystemV2(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

// TestRemoveProjectGrantsForProject: deleting a project must take every
// grant on it along — user AND group — or a recreated project under the
// same name silently resurrects the old holders as admins/editors. Other
// projects' grants must survive.
func TestRemoveProjectGrantsForProject(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	seedUser(t, d, "u1")
	seedGroupWithRole(t, d, "g1", InstanceRoleViewer)
	if _, err := d.AddProjectGrant(ctx, "doomed", "u1", "", ProjectRoleAdmin); err != nil {
		t.Fatalf("add user grant: %v", err)
	}
	if _, err := d.AddProjectGrant(ctx, "doomed", "", "g1", ProjectRoleEditor); err != nil {
		t.Fatalf("add group grant: %v", err)
	}
	if _, err := d.AddProjectGrant(ctx, "survivor", "u1", "", ProjectRoleViewer); err != nil {
		t.Fatalf("add survivor grant: %v", err)
	}

	n, err := d.RemoveProjectGrantsForProject(ctx, "doomed")
	if err != nil {
		t.Fatalf("remove grants: %v", err)
	}
	if n != 2 {
		t.Errorf("removed %d grants, want 2", n)
	}
	if left, _ := d.ListProjectGrants(ctx, "doomed"); len(left) != 0 {
		t.Errorf("doomed still has %d grants: %+v", len(left), left)
	}
	if kept, _ := d.ListProjectGrants(ctx, "survivor"); len(kept) != 1 {
		t.Errorf("survivor grants = %d, want 1 (unrelated project must be untouched)", len(kept))
	}
	// The stale-grant resurrection scenario: u1 must NOT resolve any
	// role on a reborn "doomed".
	ten, err := d.ListUserTenancy(ctx, "u1")
	if err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	if got := projectRoleOn(ten, "doomed"); got != "" {
		t.Errorf("u1 still resolves role %q on deleted project", got)
	}

	// Idempotent on a project with no grants.
	if n, err := d.RemoveProjectGrantsForProject(ctx, "doomed"); err != nil || n != 0 {
		t.Errorf("second remove = (%d, %v), want (0, nil)", n, err)
	}
}
