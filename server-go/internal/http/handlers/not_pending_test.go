package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// AUTHZ-05: an OAuth sign-up with no grant and no instance role (pending)
// must not reach repo probes or mint a personal token; a user with any
// project grant, or the projects:create perm, still can.
func TestPendingUserBlockedFromProbesAndTokens(t *testing.T) {
	d := openTestDB(t)
	seedUserNoGroup(t, d, "pending")
	seedUserWithProjectRole(t, d, "member", "p1", db.ProjectRoleEditor)
	seedUserNoGroup(t, d, "creator")

	gh := &GithubHandler{DB: d}
	admin := &AdminHandler{DB: d}
	routes := map[string]http.HandlerFunc{
		"check-repo":     gh.CheckRepoAccess,
		"detect-runtime": gh.DetectRuntime,
		"scan-addons":    gh.ScanAddons,
		"create-token":   admin.CreateMyToken,
		"repos-inspect":  RequireNotPending(d)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })).ServeHTTP,
	}
	callers := map[string]*auth.Claims{
		"pending": {UserID: "pending"},
		"member":  {UserID: "member"},
		"creator": {UserID: "creator", Permissions: []string{string(auth.PermProjectsCreate)}},
	}
	for route, h := range routes {
		for who, c := range callers {
			req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{}`))
			req = req.WithContext(auth.WithClaimsForTest(req.Context(), c))
			rec := httptest.NewRecorder()
			h(rec, req)
			blocked := rec.Code == http.StatusForbidden
			if want := who == "pending"; blocked != want {
				t.Errorf("%s as %s: status %d, blocked=%v want %v", route, who, rec.Code, blocked, want)
			}
		}
	}
}
