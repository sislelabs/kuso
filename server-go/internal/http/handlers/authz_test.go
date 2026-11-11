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
			"NotificationEvent", "BuildLog", "BuildRecord", "AlertRule",
			"NodeMetric", "LogLine", "SSHKey",
			"OAuthState", "ErrorEvent", "ErrorScannerState",
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

// TestRequirePerm_Denies covers every deny branch of the perm gate,
// including the requireAdmin wrapper.
func TestRequirePerm_Denies(t *testing.T) {
	readOnly := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermProjectRead)}}
	cases := []struct {
		name   string
		claims *auth.Claims
		gate   func(http.ResponseWriter, *http.Request) bool
		want   int
	}{
		{"RequirePerm_NoClaims_401", nil, func(w http.ResponseWriter, r *http.Request) bool {
			return requirePerm(w, r, auth.PermProjectRead)
		}, http.StatusUnauthorized},
		{"RequirePerm_MissingPerm_403", readOnly, func(w http.ResponseWriter, r *http.Request) bool {
			return requirePerm(w, r, auth.PermSettingsAdmin)
		}, http.StatusForbidden},
		{"RequireAdmin_Wraps", readOnly, requireAdmin, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			if tc.gate(rr, reqWithClaims(tc.claims)) {
				t.Fatal("expected gate to deny")
			}
			if rr.Code != tc.want {
				t.Errorf("status=%d want %d", rr.Code, tc.want)
			}
		})
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
	if !requireProjectAccess(ctx, rr, d, "any-project", db.ProjectRoleEditor) {
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
	if requireProjectAccess(ctx, rr, nil, "p1", db.ProjectRoleEditor) {
		t.Errorf("nil DB should fail closed; got pass with code=%d", rr.Code)
	}
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

// TestRequireProjectAccess_Denies403: authenticated non-admin users who
// lack a sufficient grant on the requested project are rejected.
func TestRequireProjectAccess_Denies403(t *testing.T) {
	cases := []struct {
		name    string
		seed    func(t *testing.T, d *db.DB)
		project string
		need    db.ProjectRole
	}{
		// user has no membership on p1
		{"NoMembership", func(t *testing.T, d *db.DB) { seedUserNoGroup(t, d, "u1") }, "p1", db.ProjectRoleViewer},
		// viewer should not pass editor gate
		{"RoleTooLow", func(t *testing.T, d *db.DB) { seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleViewer) }, "p1", db.ProjectRoleEditor},
		// membership on p1 should not grant access to p2
		{"DifferentProject", func(t *testing.T, d *db.DB) { seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleEditor) }, "p2", db.ProjectRoleViewer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := openTestDB(t)
			tc.seed(t, d)
			rr := httptest.NewRecorder()
			c := &auth.Claims{UserID: "u1", Permissions: []string{}}
			ctx := auth.WithClaimsForTest(context.Background(), c)
			if requireProjectAccess(ctx, rr, d, tc.project, tc.need) {
				t.Fatal("expected deny")
			}
			if rr.Code != http.StatusForbidden {
				t.Errorf("status=%d want 403", rr.Code)
			}
		})
	}
}

func TestRequireProjectAccess_RoleSufficient(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleEditor)
	rr := httptest.NewRecorder()
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	ctx := auth.WithClaimsForTest(context.Background(), c)
	if !requireProjectAccess(ctx, rr, d, "p1", db.ProjectRoleEditor) {
		t.Errorf("owner should satisfy deployer gate; body=%s", rr.Body.String())
	}
}

func TestRoleAtLeast(t *testing.T) {
	cases := []struct {
		name   string
		have   db.ProjectRole
		want   db.ProjectRole
		expect bool
	}{
		{"admin>=viewer", db.ProjectRoleAdmin, db.ProjectRoleViewer, true},
		{"admin>=editor", db.ProjectRoleAdmin, db.ProjectRoleEditor, true},
		{"admin>=admin", db.ProjectRoleAdmin, db.ProjectRoleAdmin, true},
		{"editor>=viewer", db.ProjectRoleEditor, db.ProjectRoleViewer, true},
		{"editor<admin", db.ProjectRoleEditor, db.ProjectRoleAdmin, false},
		{"viewer<editor", db.ProjectRoleViewer, db.ProjectRoleEditor, false},
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
VALUES ($1, $2, $3, 'h', false, true, 'local', NOW(), NOW())`,
		userID, userID, userID+"@x"); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

// seedUserWithProjectRole gives the user a direct ProjectGrant on the
// project with an explicit role override — the role-system-v2 path
// (the legacy projectMemberships JSON is no longer read by the resolver).
func seedUserWithProjectRole(t *testing.T, d *db.DB, userID, project string, role db.ProjectRole) {
	t.Helper()
	seedUserNoGroup(t, d, userID)
	if _, err := d.AddProjectGrant(context.Background(), project, userID, "", role); err != nil {
		t.Fatalf("seed project grant: %v", err)
	}
}
