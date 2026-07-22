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
)

// A service cron mounts the parent service's full envFromSecrets and runs
// a caller-supplied command against them — the same secret-bearing
// arbitrary-exec surface that runs.Create + the pod-shell endpoint
// admin-gate. Both the per-service Add and Update handlers must therefore
// refuse an editor (who has no secrets:read) BEFORE any cron is written.
//
// Svc/DB kube deps are left nil: the gate must reject before the handler
// ever reaches h.Svc, so a nil service that would panic on use is itself
// part of the assertion (a passing gate would blow up the test loudly).

func newEditorReq(t *testing.T, method, target string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(`{"name":"nightly","schedule":"* * * * *","command":["printenv"]}`))
	c := &auth.Claims{UserID: "u1", Permissions: []string{}}
	r = r.WithContext(auth.WithClaimsForTest(r.Context(), c))
	// chi URL params.
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "p1")
	rctx.URLParams.Add("service", "svc")
	rctx.URLParams.Add("name", "nightly")
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func TestCronAdd_EditorWithoutSecretsRead_Forbidden(t *testing.T) {
	d := openTestDB(t)
	// Editor grant — enough for ProjectRoleEditor, NOT enough for the
	// secrets:read (admin) gate the service-cron path now requires.
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleEditor)

	h := &CronsHandler{DB: d, Logger: slog.Default()}
	rr := httptest.NewRecorder()
	h.Add(rr, newEditorReq(t, http.MethodPost, "/api/projects/p1/services/svc/crons"))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("editor add: status=%d want 403 (body=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "admin role") {
		t.Errorf("expected admin-role message, got %q", rr.Body.String())
	}
}

func TestCronUpdate_EditorWithoutSecretsRead_Forbidden(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleEditor)

	h := &CronsHandler{DB: d, Logger: slog.Default()}
	rr := httptest.NewRecorder()
	h.Update(rr, newEditorReq(t, http.MethodPatch, "/api/projects/p1/services/svc/crons/nightly"))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("editor update: status=%d want 403 (body=%s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "admin role") {
		t.Errorf("expected admin-role message, got %q", rr.Body.String())
	}
}
