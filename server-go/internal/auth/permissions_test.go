package auth

import (
	"sort"
	"testing"

	"kuso/server/internal/db"
)

// hasPerm is a tiny test helper.
func hasPerm(perms []string, p Permission) bool { return Has(perms, p) }

// TestCompute_InstanceLevelOnly locks in the role-system v2 rule that
// the JWT-baked Compute() emits ONLY instance-level perms, and only for
// admins. Viewer/editor carry nothing here — their project access is
// resolved per-request, not from the token.
func TestCompute_InstanceLevelOnly(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		role      db.InstanceRole
		wantAdmin bool // expect the full instance perm set
	}{
		{"admin", db.InstanceRoleAdmin, true},
		{"editor", db.InstanceRoleEditor, false},
		{"viewer", db.InstanceRoleViewer, false},
		{"pending", db.InstanceRolePending, false},
		{"empty", db.InstanceRole(""), false},
	}
	instancePerms := []Permission{
		PermSettingsAdmin, PermSettingsRead, PermAuditRead,
		PermUserWrite, PermBillingRead, PermSystemUpdate,
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := Compute(db.GroupTenancy{InstanceRole: tc.role})
			if tc.wantAdmin {
				for _, p := range instancePerms {
					if !hasPerm(got, p) {
						t.Errorf("admin missing instance perm %s", p)
					}
				}
				// Admin must NOT carry project perms in the JWT — those
				// are resolved per-project.
				for _, p := range []Permission{PermSecretsRead, PermShellExec, PermSQLRead, PermProjectWrite} {
					if hasPerm(got, p) {
						t.Errorf("admin JWT should not carry project perm %s (resolved per-request)", p)
					}
				}
			} else if len(got) != 0 {
				t.Errorf("%s: expected NO instance perms, got %v", tc.name, got)
			}
		})
	}
}

// TestPermsForProjectRole is the core of the matrix: every effective
// project role → exactly the right permission set. This is the table
// that would have caught the env-leak / over-grant bugs.
func TestPermsForProjectRole(t *testing.T) {
	t.Parallel()

	want := map[db.ProjectRole]map[Permission]bool{
		db.ProjectRoleViewer: {
			PermProjectRead: true, PermServicesRead: true, PermAddonsRead: true,
			PermProjectWrite: false, PermServicesWrite: false, PermAddonsWrite: false,
			PermSecretsWrite: false, PermSecretsRead: false, PermShellExec: false, PermSQLRead: false,
		},
		db.ProjectRoleEditor: {
			PermProjectRead: true, PermServicesRead: true, PermAddonsRead: true,
			PermProjectWrite: true, PermServicesWrite: true, PermAddonsWrite: true,
			PermSecretsWrite: true,  // editor CAN write env vars (blind)
			PermSecretsRead:  false, // editor CANNOT read env values
			PermShellExec:    false, // editor CANNOT open a shell
			PermSQLRead:      false, // editor CANNOT use the DB console
		},
		db.ProjectRoleAdmin: {
			PermProjectRead: true, PermServicesRead: true, PermAddonsRead: true,
			PermProjectWrite: true, PermServicesWrite: true, PermAddonsWrite: true,
			PermSecretsWrite: true, PermSecretsRead: true, PermShellExec: true, PermSQLRead: true,
		},
	}

	for role, perms := range want {
		role, perms := role, perms
		t.Run(string(role), func(t *testing.T) {
			t.Parallel()
			got := PermsForProjectRole(role)
			for p, expected := range perms {
				if hasPerm(got, p) != expected {
					t.Errorf("role %s: perm %s = %v, want %v", role, p, hasPerm(got, p), expected)
				}
			}
		})
	}

	// Unknown / empty role → no perms.
	if len(PermsForProjectRole(db.ProjectRole(""))) != 0 {
		t.Error("empty role should grant no perms")
	}
}

// TestProjectRoleFor exercises the resolution rules: admin everywhere,
// highest-wins among applicable grants, invisible when no grant applies,
// and that other-project grants don't leak.
func TestProjectRoleFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		tenancy db.GroupTenancy
		project string
		want    db.ProjectRole
	}{
		{
			name:    "instance admin → admin on any project",
			tenancy: db.GroupTenancy{InstanceRole: db.InstanceRoleAdmin},
			project: "anything",
			want:    db.ProjectRoleAdmin,
		},
		{
			name:    "no grants → invisible",
			tenancy: db.GroupTenancy{InstanceRole: db.InstanceRoleEditor},
			project: "p1",
			want:    db.ProjectRole(""),
		},
		{
			name: "single editor grant",
			tenancy: db.GroupTenancy{
				InstanceRole:       db.InstanceRoleViewer,
				ProjectMemberships: []db.ProjectMembership{{Project: "p1", Role: db.ProjectRoleEditor}},
			},
			project: "p1",
			want:    db.ProjectRoleEditor,
		},
		{
			name: "highest-wins across two grants on same project",
			tenancy: db.GroupTenancy{
				InstanceRole: db.InstanceRoleViewer,
				ProjectMemberships: []db.ProjectMembership{
					{Project: "p1", Role: db.ProjectRoleViewer},
					{Project: "p1", Role: db.ProjectRoleEditor},
				},
			},
			project: "p1",
			want:    db.ProjectRoleEditor,
		},
		{
			name: "grant on other project doesn't leak",
			tenancy: db.GroupTenancy{
				InstanceRole:       db.InstanceRoleEditor,
				ProjectMemberships: []db.ProjectMembership{{Project: "p2", Role: db.ProjectRoleAdmin}},
			},
			project: "p1",
			want:    db.ProjectRole(""),
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ProjectRoleFor(tc.tenancy, tc.project); got != tc.want {
				t.Errorf("ProjectRoleFor(%s) = %q, want %q", tc.project, got, tc.want)
			}
		})
	}
}

// TestProjectsAccessible: admin → nil (all), non-admin → only granted.
func TestProjectsAccessible(t *testing.T) {
	t.Parallel()

	if got := ProjectsAccessible(db.GroupTenancy{InstanceRole: db.InstanceRoleAdmin}); got != nil {
		t.Errorf("admin should get nil (no filter), got %v", got)
	}

	t.Run("non-admin sees only granted", func(t *testing.T) {
		t.Parallel()
		got := ProjectsAccessible(db.GroupTenancy{
			InstanceRole: db.InstanceRoleEditor,
			ProjectMemberships: []db.ProjectMembership{
				{Project: "p1", Role: db.ProjectRoleEditor},
				{Project: "p2", Role: db.ProjectRoleViewer},
			},
		})
		sort.Strings(got)
		if len(got) != 2 || got[0] != "p1" || got[1] != "p2" {
			t.Errorf("got %v, want [p1 p2]", got)
		}
	})

	t.Run("non-admin with no grants sees nothing", func(t *testing.T) {
		t.Parallel()
		got := ProjectsAccessible(db.GroupTenancy{InstanceRole: db.InstanceRoleViewer})
		if len(got) != 0 {
			t.Errorf("ungranted user should see no projects, got %v", got)
		}
	})
}

// TestIsPending: admin never pending; non-admin pending iff no grants.
func TestIsPending(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		tenancy db.GroupTenancy
		want    bool
	}{
		{"admin never pending", db.GroupTenancy{InstanceRole: db.InstanceRoleAdmin}, false},
		{"editor no grants → pending", db.GroupTenancy{InstanceRole: db.InstanceRoleEditor}, true},
		{"viewer no grants → pending", db.GroupTenancy{InstanceRole: db.InstanceRoleViewer}, true},
		{"empty role no grants → pending", db.GroupTenancy{}, true},
		{
			name: "editor with a grant → not pending",
			tenancy: db.GroupTenancy{
				InstanceRole:       db.InstanceRoleEditor,
				ProjectMemberships: []db.ProjectMembership{{Project: "p1", Role: db.ProjectRoleEditor}},
			},
			want: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsPending(tc.tenancy); got != tc.want {
				t.Errorf("IsPending = %v, want %v", got, tc.want)
			}
		})
	}
}
