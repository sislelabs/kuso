package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
)

// The GitHub App enumeration endpoints leak the App's entire connected
// footprint — every installation and every repo file tree — so they're
// admin-only in-handler. These tests pin that gate: a non-admin (empty
// perms) must get 403 BEFORE the handler touches Cache/Client, so a
// zero-value handler is a valid fixture. Repo-specific probes
// (detect-runtime / scan-addons / check-repo) intentionally stay
// non-admin and are not covered here.

func TestGithubEnumeration_RejectsNonAdmin(t *testing.T) {
	t.Parallel()
	h := &GithubHandler{}
	cases := []struct {
		name    string
		path    string
		handler http.HandlerFunc
	}{
		{"ListInstallations", "/api/github/installations", h.ListInstallations},
		{"InstallationRepos", "/api/github/installations/1/repos", h.InstallationRepos},
		{"RepoTree", "/api/github/installations/1/repos/owner/repo/tree?branch=main", h.RepoTree},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			req = req.WithContext(auth.WithClaimsForTest(req.Context(),
				&auth.Claims{UserID: "u1", Permissions: []string{}}))
			rr := httptest.NewRecorder()
			tc.handler(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s status=%d want 403; body=%s", tc.name, rr.Code, rr.Body.String())
			}
		})
	}
}

func TestGithubEnumeration_AdminPasses(t *testing.T) {
	t.Parallel()
	// With Cache == nil, ListInstallations returns an empty list (200)
	// once the admin gate passes — proving the gate, not the data path.
	h := &GithubHandler{}
	req := httptest.NewRequest(http.MethodGet, "/api/github/installations", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	h.ListInstallations(rr, req)
	if rr.Code != http.StatusOK {
		t.Errorf("admin ListInstallations status=%d want 200; body=%s", rr.Code, rr.Body.String())
	}
}
