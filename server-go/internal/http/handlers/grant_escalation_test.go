package handlers_test

import (
	"net/http"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// A "user manager" holds user:write (the seeded admin Role grants it)
// but not settings:admin. It must not be able to confer access beyond
// its own, and must not change its own access at all.

func userManagerToken(t *testing.T, iss *auth.Issuer, id string) string {
	return mintToken(t, iss, id, auth.PermUserWrite)
}

func TestGrantEscalation_UserManagerCannotMakeSelfInstanceAdmin(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedGrantsUser(t, d, "bob")
	bob := userManagerToken(t, iss, "bob")

	rr := do(t, r, http.MethodPut, "/api/users/bob/instance-role", bob, `{"role":"admin"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("self instance-role admin: code=%d want 403 body=%s", rr.Code, rr.Body.String())
	}
	tn, err := d.ListUserTenancy(t.Context(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	if tn.InstanceRole != "" {
		t.Fatalf("bob instance role = %q, want unchanged empty", tn.InstanceRole)
	}
}

func seedGroup(t *testing.T, d *db.DB, id string, role db.InstanceRole) {
	t.Helper()
	if err := d.CreateGroup(t.Context(), id, id, ""); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := d.SetGroupTenancy(t.Context(), id, db.GroupTenancy{InstanceRole: role}); err != nil {
		t.Fatalf("seed group tenancy: %v", err)
	}
}

func seedRole(t *testing.T, d *db.DB, id string, perms ...db.PermissionInput) {
	t.Helper()
	if err := d.CreateRole(t.Context(), id, id, "", perms); err != nil {
		t.Fatalf("seed role: %v", err)
	}
}

func TestGrantEscalation_UserManagerRefused(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	for _, u := range []string{"bob", "carol", "root"} {
		seedGrantsUser(t, d, u)
	}
	if err := d.SetUserInstanceRole(t.Context(), "root", db.InstanceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	seedGroup(t, d, "admins", db.InstanceRoleAdmin)
	// The seeded "admin" Role shape: user:write plus perms bob lacks.
	seedRole(t, d, "adminrole", db.PermissionInput{Resource: "user", Action: "write"}, db.PermissionInput{Resource: "app", Action: "write"})
	bob := userManagerToken(t, iss, "bob")

	cases := []struct{ name, method, path, body string }{
		{"other user instance admin", http.MethodPut, "/api/users/carol/instance-role", `{"role":"admin"}`},
		{"other user instance editor", http.MethodPut, "/api/users/carol/instance-role", `{"role":"editor"}`},
		{"group instance admin", http.MethodPut, "/api/groups/admins/instance-role", `{"role":"admin"}`},
		{"group tenancy admin", http.MethodPut, "/api/groups/admins/tenancy", `{"instanceRole":"admin"}`},
		{"self into group", http.MethodPost, "/api/groups/admins/members/bob", ``},
		{"self instance role even within own perms", http.MethodPut, "/api/users/bob/instance-role", `{"role":"viewer"}`},
		{"other into admin group", http.MethodPost, "/api/groups/admins/members/carol", ``},
		{"assign role beyond own", http.MethodPut, "/api/users/id/carol", `{"roleId":"adminrole"}`},
		{"create user with role beyond own", http.MethodPost, "/api/users", `{"username":"eve","email":"eve@x","password":"password1","roleId":"adminrole"}`},
		{"define role beyond own", http.MethodPut, "/api/roles/adminrole", `{"name":"adminrole","permissions":[{"resource":"app","action":"write"}]}`},
		{"admin invite", http.MethodPost, "/api/invites", `{"instanceRole":"admin"}`},
		{"invite into admin group", http.MethodPost, "/api/invites", `{"groupId":"admins"}`},
		{"project grant to self", http.MethodPost, "/api/projects/p1/grants", `{"userId":"bob","role":"viewer"}`},
		{"project admin grant beyond own", http.MethodPost, "/api/projects/p1/grants", `{"userId":"carol","role":"admin"}`},
		{"reset instance admin password", http.MethodPut, "/api/users/id/root/password", `{"password":"hijacked1"}`},
		{"mint token for instance admin", http.MethodPost, "/api/tokens/user/root", `{"name":"x"}`},
	}
	for _, c := range cases {
		rr := do(t, r, c.method, c.path, bob, c.body)
		if rr.Code != http.StatusForbidden {
			t.Errorf("%s: code=%d want 403 body=%s", c.name, rr.Code, rr.Body.String())
		}
	}

	tn, _ := d.ListUserTenancy(t.Context(), "carol")
	if tn.InstanceRole != "" || len(tn.ProjectMemberships) != 0 {
		t.Errorf("carol gained access: %+v", tn)
	}
	if tn, _ := d.ListUserTenancy(t.Context(), "bob"); tn.InstanceRole != "" || len(tn.ProjectMemberships) != 0 {
		t.Errorf("bob gained access: %+v", tn)
	}
}

func TestGrantEscalation_UserManagerMayGrantWithinOwnPerms(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedGrantsUser(t, d, "bob")
	seedGrantsUser(t, d, "carol")
	seedGroup(t, d, "viewers", db.InstanceRoleViewer)
	seedRole(t, d, "usermgr", db.PermissionInput{Resource: "user", Action: "write"})
	bob := userManagerToken(t, iss, "bob")

	for _, c := range []struct{ name, method, path, body string }{
		{"other user viewer", http.MethodPut, "/api/users/carol/instance-role", `{"role":"viewer"}`},
		{"other into viewer group", http.MethodPost, "/api/groups/viewers/members/carol", ``},
		{"assign role within own", http.MethodPut, "/api/users/id/carol", `{"roleId":"usermgr"}`},
	} {
		if rr := do(t, r, c.method, c.path, bob, c.body); rr.Code/100 != 2 {
			t.Errorf("%s: code=%d want 2xx body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
}

func TestGrantEscalation_AdminKeepsFullAbility(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	for _, u := range []string{"root", "carol", "dave"} {
		seedGrantsUser(t, d, u)
	}
	seedGroup(t, d, "admins", db.InstanceRoleAdmin)
	seedRole(t, d, "adminrole", db.PermissionInput{Resource: "user", Action: "write"}, db.PermissionInput{Resource: "app", Action: "write"})
	root := mintToken(t, iss, "root", auth.PermSettingsAdmin, auth.PermUserWrite)

	for _, c := range []struct{ name, method, path, body string }{
		{"other user instance admin", http.MethodPut, "/api/users/carol/instance-role", `{"role":"admin"}`},
		{"own instance role", http.MethodPut, "/api/users/root/instance-role", `{"role":"admin"}`},
		{"group tenancy admin", http.MethodPut, "/api/groups/admins/tenancy", `{"instanceRole":"admin"}`},
		{"self into group", http.MethodPost, "/api/groups/admins/members/root", ``},
		{"assign admin role", http.MethodPut, "/api/users/id/dave", `{"roleId":"adminrole"}`},
		{"define role", http.MethodPut, "/api/roles/adminrole", `{"name":"adminrole","permissions":[{"resource":"app","action":"write"}]}`},
		{"admin invite", http.MethodPost, "/api/invites", `{"instanceRole":"admin","groupId":"admins"}`},
		{"project admin grant", http.MethodPost, "/api/projects/p1/grants", `{"userId":"carol","role":"admin"}`},
		{"reset admin password", http.MethodPut, "/api/users/id/carol/password", `{"password":"newpass12"}`},
		{"mint token for admin", http.MethodPost, "/api/tokens/user/carol", `{"name":"x"}`},
	} {
		if rr := do(t, r, c.method, c.path, root, c.body); rr.Code/100 != 2 {
			t.Errorf("%s: code=%d want 2xx body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
}
