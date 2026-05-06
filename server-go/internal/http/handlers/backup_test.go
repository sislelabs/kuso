package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
)

// v0.9: backup endpoints became 501 stubs. The SQLite-file shape that
// the pre-v0.9 tests exercised is gone — Postgres backups belong to
// pg_dump / RDS snapshots / pgBackRest, not an HTTPS round-trip. The
// remaining tests just confirm the routes still mount and gate on
// admin role.

func TestBackup_DownloadReturns501ForAdmin(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	(&httphandlers.BackupHandler{}).Mount(r)

	req := httptest.NewRequest(http.MethodGet, "/api/admin/backup", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status=%d want 501; body=%s", rr.Code, rr.Body.String())
	}
}

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

func TestBackup_RestoreReturns501ForAdmin(t *testing.T) {
	t.Parallel()
	r := chi.NewRouter()
	r.Use(injectClaims(&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	(&httphandlers.BackupHandler{}).Mount(r)

	req := httptest.NewRequest(http.MethodPost, "/api/admin/restore", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("status=%d want 501; body=%s", rr.Code, rr.Body.String())
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
