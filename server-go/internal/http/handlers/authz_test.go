package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// authz.go is the chokepoint that projects/kubernetes/invites/addons all
// funnel through. The handlers themselves call kube and are awkward to
// test in isolation; the gate is pure HTTP and DB.

// openTestDB returns a Postgres-backed *db.DB or skips when
// KUSO_TEST_PG_DSN isn't set. Each invocation truncates every table
// so tests are isolated even when run in series.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set; skipping postgres-backed test")
	}
	d, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if _, err := d.DB.Exec(`
		TRUNCATE TABLE
			"_PermissionToToken", "_PermissionToRole", "_UserToUserGroup",
			"InviteRedemption", "Invite",
			"NotificationEvent", "BuildLog", "AlertRule",
			"NodeMetric", "LogLine", "SSHKey",
			"Audit", "Token", "Permission",
			"Notification", "GithubInstallation", "GithubUserLink",
			"User", "UserGroup", "Role"
		RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func reqWithClaims(claims *auth.Claims) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	if claims != nil {
		r = r.WithContext(auth.WithClaimsForTest(r.Context(), claims))
	}
	return r
}

func TestRequirePerm_NoClaims_401(t *testing.T) {
	rr := httptest.NewRecorder()
	if requirePerm(rr, reqWithClaims(nil), auth.PermProjectRead) {
		t.Fatal("expected gate to deny")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestRequirePerm_MissingPerm_403(t *testing.T) {
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermProjectRead)}}
	if requirePerm(rr, reqWithClaims(c), auth.PermSettingsAdmin) {
		t.Fatal("expected gate to deny")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

func TestRequirePerm_Granted_PassesThrough(t *testing.T) {
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	if !requirePerm(rr, reqWithClaims(c), auth.PermSettingsAdmin) {
		t.Errorf("expected gate to pass")
	}
	if rr.Code != http.StatusOK {
		// Recorder default is 200 when nothing was written.
		t.Errorf("gate wrote response on success: %d", rr.Code)
	}
}

func TestRequireAdmin_Wraps(t *testing.T) {
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermProjectRead)}}
	if requireAdmin(rr, reqWithClaims(c)) {
		t.Errorf("non-admin passed requireAdmin")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

// requireProjectAccess: ladder of cases.

func TestRequireProjectAccess_NoClaims_401(t *testing.T) {
	d := openTestDB(t)
	rr := httptest.NewRecorder()
	if requireProjectAccess(context.Background(), rr, d, "p1", db.ProjectRoleViewer) {
		t.Fatal("expected deny")
	}
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestRequireProjectAccess_AdminBypasses(t *testing.T) {
	d := openTestDB(t)
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if !requireProjectAccess(ctx, rr, d, "any-project", db.ProjectRoleOwner) {
		t.Errorf("admin should bypass project access; got body=%s", rr.Body.String())
	}
}

func TestRequireProjectAccess_NilDB_FailsClosed(t *testing.T) {
	// v0.8.13 reversed the legacy fail-open: a nil DB is now treated
	// as a misconfigured handler and rejected with 403. Without this,
	// any handler that pre-dated the tenancy table (or skipped wiring
	// DB) would let any authenticated JWT bypass project membership.
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if requireProjectAccess(ctx, rr, nil, "p1", db.ProjectRoleOwner) {
		t.Errorf("nil DB should fail closed; got pass with code=%d", rr.Code)
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

func TestRequireProjectAccess_NoMembership_403(t *testing.T) {
	d := openTestDB(t)
	seedUserNoGroup(t, d, "u1")
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if requireProjectAccess(ctx, rr, d, "p1", db.ProjectRoleViewer) {
		t.Fatal("expected deny — user has no membership on p1")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

func TestRequireProjectAccess_RoleTooLow_403(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleViewer)
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if requireProjectAccess(ctx, rr, d, "p1", db.ProjectRoleOwner) {
		t.Fatal("viewer should not pass owner gate")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

func TestRequireProjectAccess_RoleSufficient(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleOwner)
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if !requireProjectAccess(ctx, rr, d, "p1", db.ProjectRoleDeployer) {
		t.Errorf("owner should satisfy deployer gate; body=%s", rr.Body.String())
	}
}

func TestRequireProjectAccess_DifferentProject_403(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleOwner)
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if requireProjectAccess(ctx, rr, d, "p2", db.ProjectRoleViewer) {
		t.Fatal("membership on p1 should not grant access to p2")
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		name   string
		have   db.ProjectRole
		want   db.ProjectRole
		expect bool
	}{
		{"owner>=viewer", db.ProjectRoleOwner, db.ProjectRoleViewer, true},
		{"owner>=deployer", db.ProjectRoleOwner, db.ProjectRoleDeployer, true},
		{"owner>=owner", db.ProjectRoleOwner, db.ProjectRoleOwner, true},
		{"deployer>=viewer", db.ProjectRoleDeployer, db.ProjectRoleViewer, true},
		{"deployer<owner", db.ProjectRoleDeployer, db.ProjectRoleOwner, false},
		{"viewer<deployer", db.ProjectRoleViewer, db.ProjectRoleDeployer, false},
		{"empty<viewer", db.ProjectRole(""), db.ProjectRoleViewer, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := roleAtLeast(tc.have, tc.want); got != tc.expect {
				t.Errorf("roleAtLeast(%q,%q)=%v want %v", tc.have, tc.want, got, tc.expect)
			}
		})
	}
}

// --- seed helpers -----------------------------------------------------

func seedUserNoGroup(t *testing.T, d *db.DB, userID string) {
	t.Helper()
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES (?, ?, ?, 'h', false, true, 'local', NOW(), NOW())`,
		userID, userID, userID+"@x"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func seedUserWithProjectRole(t *testing.T, d *db.DB, userID, project string, role db.ProjectRole) {
	t.Helper()
	seedUserNoGroup(t, d, userID)
	groupID := userID + "-g"
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "UserGroup" (id, name, description, "instanceRole", "projectMemberships", "createdAt", "updatedAt")
VALUES (?, ?, '', 'member', ?, NOW(), NOW())`,
		groupID, groupID,
		`[{"project":"`+project+`","role":"`+string(role)+`"}]`); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "_UserToUserGroup" ("A", "B") VALUES (?, ?)`, userID, groupID); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
}
