package handlers

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// SEC-1: a forced rollback puts another branch's image on the env with
// its secrets, so an editor may not force one. The gate answers before
// the service is touched, which is why Svc can stay nil here.
func TestRollbackForceNeedsProjectAdmin(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "ed-force", "p1", db.ProjectRoleEditor)
	h := &BuildsHandler{DB: d, Logger: slog.Default()}
	r := chi.NewRouter()
	r.Post("/api/projects/{project}/services/{service}/builds/{build}/rollback", h.Rollback)
	for _, c := range []struct{ name, query, body string }{
		{"query force", "?force=true", ""},
		{"body force", "", `{"force":true}`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/services/web/builds/b1/rollback"+c.query, strings.NewReader(c.body))
		req = req.WithContext(auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: "ed-force", Username: "ed"}))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "project admin") {
			t.Errorf("%s: status %d (%s), want 403 naming the admin role", c.name, rr.Code, rr.Body.String())
		}
	}
}
