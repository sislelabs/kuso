package handlers_test

import (
	"context"
	"net/http"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// SEC-1 (2026-10-07 review): the "admin" Role seeded on first boot
// carries user:write, audit:read and settings:read. A user holding that
// Role without being an instance admin could deactivate/delete admins,
// demote the admin group, clear direct admin roles, and reset passwords
// / mint tokens for project admins.

// seedTakeoverFixture builds: root (admin-group member), boss (direct
// instance admin), pa (admin on project p1), mallory (seeded admin Role,
// no instance role) and plain (no access). The Role is the real one
// BootstrapAdmin seeds, not a hand-built copy.
func seedTakeoverFixture(t *testing.T, d *db.DB) {
	t.Helper()
	ctx := context.Background()
	if err := d.BootstrapAdmin(ctx, "admin", "", "h"); err != nil {
		t.Fatal(err)
	}
	var seededRole string
	if err := d.QueryRowContext(ctx, `SELECT id FROM "Role" WHERE name = 'admin'`).Scan(&seededRole); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"root", "boss", "pa", "mallory", "plain"} {
		seedGrantsUser(t, d, u)
	}
	seedGroup(t, d, "admins", db.InstanceRoleAdmin)
	if err := d.AddUserToGroup(ctx, "root", "admins"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserInstanceRole(ctx, "boss", db.InstanceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddProjectGrant(ctx, "p1", "pa", "", db.ProjectRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := d.UpdateUser(ctx, "mallory", db.UpdateUserInput{RoleID: &seededRole}); err != nil {
		t.Fatal(err)
	}
}

// takeoverCalls are the seven calls the review proved returned 2xx.
var takeoverCalls = []struct{ name, method, path, body string }{
	{"deactivate group admin", http.MethodPut, "/api/users/id/root", `{"isActive":false}`},
	{"clear direct admin role", http.MethodPut, "/api/users/boss/instance-role", `{"role":""}`},
	{"demote admin group", http.MethodPut, "/api/groups/admins/instance-role", `{"role":""}`},
	{"remove admin from admin group", http.MethodDelete, "/api/groups/admins/members/root", ``},
	{"delete direct admin", http.MethodDelete, "/api/users/id/boss", ``},
	{"reset project admin password", http.MethodPut, "/api/users/id/pa/password", `{"password":"hijacked1"}`},
	{"mint token for project admin", http.MethodPost, "/api/tokens/user/pa", `{"name":"x"}`},
}

func assertNoTakeover(t *testing.T, d *db.DB) {
	t.Helper()
	ctx := context.Background()
	for _, u := range []string{"root", "boss"} {
		ten, err := d.ListUserTenancy(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		if ten.InstanceRole != db.InstanceRoleAdmin {
			t.Errorf("%s lost instance admin: %+v", u, ten)
		}
	}
	root, err := d.FindUserByID(ctx, "root")
	if err != nil || !root.IsActive {
		t.Errorf("root deactivated or missing: %+v err=%v", root, err)
	}
	pa, err := d.FindUserByID(ctx, "pa")
	if err != nil || pa.Password != "h" {
		t.Errorf("project admin password changed: err=%v", err)
	}
}

// Root cause: role-derived reserved perms. Uses the production
// per-request resolver, so the token's baked perms don't matter.
func TestSEC1_SeededAdminRoleGrantsNoReservedPerms(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	iss.SetPermissionResolver(func(ctx context.Context, c *auth.Claims) ([]string, bool) {
		perms, err := auth.EffectivePermissions(ctx, d, c.UserID)
		return perms, err == nil
	})
	seedTakeoverFixture(t, d)

	perms, err := auth.EffectivePermissions(context.Background(), d, "mallory")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []auth.Permission{auth.PermUserWrite, auth.PermAuditRead, auth.PermSettingsRead} {
		if auth.Has(perms, p) {
			t.Errorf("mallory resolved reserved %s from her role: %v", p, perms)
		}
	}
	if !auth.Has(perms, "app:write") {
		t.Errorf("non-reserved role perm dropped: %v", perms)
	}

	tok := mintToken(t, iss, "mallory", auth.PermUserWrite)
	for _, c := range takeoverCalls {
		if rr := do(t, r, c.method, c.path, tok, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("%s: code=%d want 403 body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
	assertNoTakeover(t, d)
}

// Defence in depth: even a principal that really holds user:write
// without being an instance admin can't act on anyone who outranks it.
func TestSEC1_UserWriteHolderCannotActOnOutrankingUsers(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedTakeoverFixture(t, d)
	tok := mintToken(t, iss, "mallory", auth.PermUserWrite)

	for _, c := range takeoverCalls {
		if rr := do(t, r, c.method, c.path, tok, c.body); rr.Code != http.StatusForbidden {
			t.Errorf("%s: code=%d want 403 body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
	assertNoTakeover(t, d)

	// A user with no access of their own is still manageable.
	if rr := do(t, r, http.MethodPut, "/api/users/id/plain/password", tok, `{"password":"newpass12"}`); rr.Code != http.StatusNoContent {
		t.Errorf("reset plain user password: code=%d want 204 body=%s", rr.Code, rr.Body.String())
	}
	if rr := do(t, r, http.MethodPut, "/api/users/id/plain", tok, `{"isActive":false}`); rr.Code != http.StatusNoContent {
		t.Errorf("deactivate plain user: code=%d want 204 body=%s", rr.Code, rr.Body.String())
	}
}

func TestSEC1_AdminStillManagesAdmins(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	seedTakeoverFixture(t, d)
	seedGrantsUser(t, d, "chief")
	tok := mintToken(t, iss, "chief", auth.PermSettingsAdmin, auth.PermUserWrite)

	for _, c := range takeoverCalls {
		if rr := do(t, r, c.method, c.path, tok, c.body); rr.Code/100 != 2 {
			t.Errorf("%s: code=%d want 2xx body=%s", c.name, rr.Code, rr.Body.String())
		}
	}
}
