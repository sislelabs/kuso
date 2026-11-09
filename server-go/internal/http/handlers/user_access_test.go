package handlers_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

type accessSourceJSON struct {
	Via          string `json:"via"`
	GroupID      string `json:"groupId"`
	GroupName    string `json:"groupName"`
	Role         string `json:"role"`
	RoleOverride string `json:"roleOverride"`
}

type userAccessJSON struct {
	UserID              string             `json:"userId"`
	Username            string             `json:"username"`
	InstanceRole        string             `json:"instanceRole"`
	InstanceRoleSources []accessSourceJSON `json:"instanceRoleSources"`
	Groups              []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		InstanceRole string `json:"instanceRole"`
	} `json:"groups"`
	AllProjects bool `json:"allProjects"`
	Projects    []struct {
		Project string             `json:"project"`
		Role    string             `json:"role"`
		Sources []accessSourceJSON `json:"sources"`
	} `json:"projects"`
	Pending bool `json:"pending"`
}

func getUserAccess(t *testing.T, r http.Handler, tok, userID string) userAccessJSON {
	t.Helper()
	rr := do(t, r, http.MethodGet, "/api/users/"+userID+"/access", tok, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("GET access %s: code=%d body=%s", userID, rr.Code, rr.Body.String())
	}
	var out userAccessJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func TestUserAccess_ExplainsSources(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	ctx := t.Context()
	seedGrantsUser(t, d, "alice")
	seedGrantsUser(t, d, "boss")
	seedGroup(t, d, "devs", db.InstanceRoleViewer)
	if err := d.AddUserToGroup(ctx, "alice", "devs"); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserInstanceRole(ctx, "alice", db.InstanceRoleEditor); err != nil {
		t.Fatal(err)
	}
	// shop: group grant inherits (→ editor via alice's direct role) and a
	// direct viewer override; highest wins → editor. blog: group admin override.
	if _, err := d.AddProjectGrant(ctx, "shop", "", "devs", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddProjectGrant(ctx, "shop", "alice", "", db.ProjectRoleViewer); err != nil {
		t.Fatal(err)
	}
	if _, err := d.AddProjectGrant(ctx, "blog", "", "devs", db.ProjectRoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := d.SetUserInstanceRole(ctx, "boss", db.InstanceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	admin := mintToken(t, iss, "admin", auth.PermSettingsAdmin, auth.PermUserWrite)

	a := getUserAccess(t, r, admin, "alice")
	if a.InstanceRole != "editor" || a.AllProjects || a.Pending {
		t.Errorf("alice instance=%q all=%v pending=%v, want editor/false/false", a.InstanceRole, a.AllProjects, a.Pending)
	}
	if !slices.Equal(a.InstanceRoleSources, []accessSourceJSON{
		{Via: "direct", Role: "editor"},
		{Via: "group", GroupID: "devs", GroupName: "devs", Role: "viewer"},
	}) {
		t.Errorf("instance sources = %+v", a.InstanceRoleSources)
	}
	if len(a.Groups) != 1 || a.Groups[0].Name != "devs" {
		t.Errorf("groups = %+v, want [devs]", a.Groups)
	}
	roles := map[string]string{}
	for _, p := range a.Projects {
		roles[p.Project] = p.Role
		if p.Project == "shop" && !slices.Equal(p.Sources, []accessSourceJSON{
			{Via: "direct", Role: "viewer", RoleOverride: "viewer"},
			{Via: "group", GroupID: "devs", GroupName: "devs", Role: "editor"},
		}) {
			t.Errorf("shop sources = %+v", p.Sources)
		}
	}
	if roles["shop"] != "editor" || roles["blog"] != "admin" || len(roles) != 2 {
		t.Errorf("project roles = %v, want shop=editor blog=admin", roles)
	}
	// The endpoint must agree with what the authorizer resolves.
	tn, _ := d.ListUserTenancy(ctx, "alice")
	for _, m := range tn.ProjectMemberships {
		if got := auth.ProjectRoleFor(tn, m.Project); string(got) != roles[m.Project] {
			t.Errorf("%s: authorizer=%q endpoint=%q", m.Project, got, roles[m.Project])
		}
	}

	b := getUserAccess(t, r, admin, "boss")
	if b.InstanceRole != "admin" || !b.AllProjects {
		t.Errorf("boss instance=%q all=%v, want admin/true", b.InstanceRole, b.AllProjects)
	}

	if rr := do(t, r, http.MethodGet, "/api/users/nobody/access", admin, ""); rr.Code != http.StatusNotFound {
		t.Errorf("unknown user: code=%d want 404", rr.Code)
	}
	viewer := mintToken(t, iss, "alice")
	if rr := do(t, r, http.MethodGet, "/api/users/boss/access", viewer, ""); rr.Code != http.StatusForbidden {
		t.Errorf("non-admin: code=%d want 403", rr.Code)
	}
}

// Approving a pending user — via group membership or a direct instance
// role — drops their kuso-pending membership.
func TestApprovalClearsPending(t *testing.T) {
	r, d, iss := newGrantsServer(t)
	ctx := t.Context()
	for _, u := range []string{"root", "viagroup", "viarole", "cleared"} {
		seedGrantsUser(t, d, u)
	}
	if err := d.SetUserInstanceRole(ctx, "root", db.InstanceRoleAdmin); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"viagroup", "viarole", "cleared"} {
		if err := d.AddUserToPendingGroup(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	seedGroup(t, d, "halks", "")
	root := mintToken(t, iss, "root", auth.PermSettingsAdmin, auth.PermUserWrite)

	groupsOf := func(u string) []string {
		names, err := d.UserGroupNames(ctx, u)
		if err != nil {
			t.Fatal(err)
		}
		return names
	}

	// Re-adding to the pending group itself must not strip it.
	if rr := do(t, r, http.MethodPost, "/api/groups/grp-pending/members/viagroup", root, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("add to pending: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := groupsOf("viagroup"); !slices.Equal(got, []string{db.PendingGroupName}) {
		t.Errorf("after re-add pending: %v", got)
	}

	if rr := do(t, r, http.MethodPost, "/api/groups/halks/members/viagroup", root, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("add member: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := groupsOf("viagroup"); !slices.Equal(got, []string{"halks"}) {
		t.Errorf("viagroup groups = %v, want [halks]", got)
	}

	// Clearing a role is not an approval.
	if rr := do(t, r, http.MethodPut, "/api/users/cleared/instance-role", root, `{"role":""}`); rr.Code != http.StatusNoContent {
		t.Fatalf("clear role: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := groupsOf("cleared"); !slices.Equal(got, []string{db.PendingGroupName}) {
		t.Errorf("cleared groups = %v, want still pending", got)
	}

	if rr := do(t, r, http.MethodPut, "/api/users/viarole/instance-role", root, `{"role":"viewer"}`); rr.Code != http.StatusNoContent {
		t.Fatalf("set role: code=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := groupsOf("viarole"); len(got) != 0 {
		t.Errorf("viarole groups = %v, want none", got)
	}
}
