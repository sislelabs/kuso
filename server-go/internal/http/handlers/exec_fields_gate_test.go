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
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

type execGateFakeSvc struct {
	ProjectsAPI
	patched, added bool
}

func (f *execGateFakeSvc) GetService(context.Context, string, string) (*kube.KusoService, error) {
	return &kube.KusoService{Spec: kube.KusoServiceSpec{Command: []string{"./run"}, Port: 8080}}, nil
}

func (f *execGateFakeSvc) PatchService(context.Context, string, string, projects.PatchServiceRequest) (*kube.KusoService, error) {
	f.patched = true
	return &kube.KusoService{}, nil
}

func (f *execGateFakeSvc) AddService(context.Context, string, projects.CreateServiceRequest) (*kube.KusoService, error) {
	f.added = true
	return &kube.KusoService{}, nil
}

func (f *execGateFakeSvc) EnrichServiceWithManagedSecretKeys(context.Context, string, string, *kube.KusoService) {
}

// SEC-1 (2026-11-10): an editor could rewrite a service's command (or
// image, release hook, build command) and read the secrets it printed
// from the logs, the capability runs and the pod shell admin-gate.
func TestServiceExecFieldsNeedProjectAdmin(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "ed-exec", "p1", db.ProjectRoleEditor)
	editor := &auth.Claims{UserID: "ed-exec", Username: "ed"}
	admin := &auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}}

	do := func(claims *auth.Claims, method, path, body string) (*execGateFakeSvc, *httptest.ResponseRecorder) {
		fake := &execGateFakeSvc{}
		h := &ProjectsHandler{Svc: fake, DB: d, Logger: slog.Default(), Namespace: "kuso"}
		r := chi.NewRouter()
		r.Patch("/api/projects/{project}/services/{service}", h.PatchService)
		r.Post("/api/projects/{project}/services", h.AddService)
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return fake, rr
	}
	const svc = "/api/projects/p1/services/web"

	cases := []struct {
		name   string
		claims *auth.Claims
		body   string
		want   int
	}{
		{"editor changes command", editor, `{"command":["sh","-c","echo $SECRET"]}`, http.StatusForbidden},
		{"editor sets image", editor, `{"image":{"repository":"busybox","tag":"1"}}`, http.StatusForbidden},
		{"editor sets release hook", editor, `{"release":{"command":["printenv"]}}`, http.StatusForbidden},
		{"editor sets static buildCmd", editor, `{"static":{"buildCmd":"printenv"}}`, http.StatusForbidden},
		{"editor re-sends unchanged command", editor, `{"command":["./run"],"port":3000}`, http.StatusOK},
		{"editor clears release hook", editor, `{"release":{"clear":true}}`, http.StatusOK},
		{"editor changes port", editor, `{"port":3000}`, http.StatusOK},
		{"admin changes command", admin, `{"command":["sh","-c","echo hi"]}`, http.StatusOK},
	}
	for _, c := range cases {
		fake, rr := do(c.claims, http.MethodPatch, svc, c.body)
		if rr.Code != c.want {
			t.Errorf("%s: status %d (%s), want %d", c.name, rr.Code, rr.Body.String(), c.want)
		}
		if fake.patched != (c.want == http.StatusOK) {
			t.Errorf("%s: PatchService called = %v", c.name, fake.patched)
		}
	}

	if fake, rr := do(editor, http.MethodPost, "/api/projects/p1/services",
		`{"name":"x","runtime":"image","image":{"repository":"busybox"},"command":["printenv"]}`); rr.Code != http.StatusForbidden || fake.added {
		t.Errorf("editor create image service: status %d added=%v, want 403 and no create", rr.Code, fake.added)
	}
}
