package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/projects"
)

type probeFakeSvc struct {
	ProjectsAPI
	calls int
}

func (f *probeFakeSvc) TestHealthcheck(context.Context, string, string, projects.HealthProbeRequest) (*projects.HealthProbeResult, error) {
	f.calls++
	return &projects.HealthProbeResult{Status: 200, Pod: "p"}, nil
}

func probeRequest(t *testing.T, h *ProjectsHandler, claims *auth.Claims) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	r.Post("/api/projects/{project}/services/{service}/healthcheck/test", h.TestHealthcheck)
	req := httptest.NewRequest(http.MethodPost, "/api/projects/p1/services/web/healthcheck/test",
		strings.NewReader(`{"path":"/healthz"}`))
	req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestHealthcheckProbe_NonMemberForbidden(t *testing.T) {
	t.Parallel()
	fake := &probeFakeSvc{}
	h := &ProjectsHandler{Svc: fake, Logger: slog.Default(), Namespace: "kuso"}
	rr := probeRequest(t, h, &auth.Claims{UserID: "outsider"})
	if rr.Code != http.StatusForbidden || fake.calls != 0 {
		t.Fatalf("status = %d calls = %d, want 403 before any probe", rr.Code, fake.calls)
	}
	rr = probeRequest(t, h, &auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}})
	if rr.Code != http.StatusOK || fake.calls != 1 {
		t.Fatalf("admin status = %d calls = %d, want 200", rr.Code, fake.calls)
	}
}

// PG-backed: a project viewer can read the settings but not run the probe;
// an editor (who can save the health check) can.
func TestHealthcheckProbe_ViewerForbiddenEditorAllowed(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "viewer", "p1", db.ProjectRoleViewer)
	seedUserWithProjectRole(t, d, "editor", "p1", db.ProjectRoleEditor)
	fake := &probeFakeSvc{}
	h := &ProjectsHandler{Svc: fake, DB: d, Logger: slog.Default(), Namespace: "kuso"}

	if rr := probeRequest(t, h, &auth.Claims{UserID: "viewer", Permissions: []string{}}); rr.Code != http.StatusForbidden || fake.calls != 0 {
		t.Fatalf("viewer status = %d calls = %d, want 403", rr.Code, fake.calls)
	}
	if rr := probeRequest(t, h, &auth.Claims{UserID: "editor", Permissions: []string{}}); rr.Code != http.StatusOK || fake.calls != 1 {
		t.Fatalf("editor status = %d calls = %d, want 200", rr.Code, fake.calls)
	}
}
