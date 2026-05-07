package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
)

// v0.9.38: backup/restore endpoints are real again, but the
// integration surface is hard to unit-test (pg_dump on $PATH, kube
// Job creation). These tests just exercise the mount + admin gate;
// end-to-end coverage lives in the e2e harness.

func TestBackup_DownloadRejectsNonAdmin(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{}}))
	(&httphandlers.BackupHandler{}).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestBackup_RestoreRejectsNonAdmin(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{}}))
	(&httphandlers.BackupHandler{}).Mount(r)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/restore", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestBackup_RestoreNoKubeReturns503(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	(&httphandlers.BackupHandler{}).Mount(r)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/restore", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d want 503 (no kube); body=%s", rr.Code, rr.Body.String())
	}
}

// injectClaims is a tiny middleware factory for tests. Production has
// the JWT middleware do this from the bearer token; here we shortcut.
func injectClaims(c *auth.Claims) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(auth.WithClaimsForTest(req.Context(), c)))
		})
	}
}
