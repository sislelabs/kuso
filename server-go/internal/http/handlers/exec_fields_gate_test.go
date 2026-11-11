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
	return &kube.KusoService{Spec: kube.KusoServiceSpec{Command: []string{"./run"}, Port: 8080,
		EnvVars: []kube.KusoEnvVar{{Name: "NODE_OPTIONS", Value: "--max-old-space-size=512"}}}}, nil
}

// Every write path flips patched, so one flag tells whether the gate let
// the request through.
func (f *execGateFakeSvc) SetEnvVar(context.Context, string, string, string, projects.SetEnvVarRequest) (*kube.KusoService, error) {
	f.patched = true
	return &kube.KusoService{}, nil
}

func (f *execGateFakeSvc) SetEnvValue(context.Context, string, string, string, string) (*kube.KusoService, error) {
	f.patched = true
	return &kube.KusoService{}, nil
}

func (f *execGateFakeSvc) SetEnvWithOpts(context.Context, string, string, []projects.EnvVar, projects.SetEnvOpts) error {
	f.patched = true
	return nil
}

func (f *execGateFakeSvc) ApplyServiceChanges(context.Context, string, string, projects.ServiceChangesRequest) (*projects.ServiceChangesResult, error) {
	f.patched = true
	return &projects.ServiceChangesResult{}, nil
}

func (f *execGateFakeSvc) SetEnvScopedVar(context.Context, string, string, string, string, projects.SetEnvVarRequest) (*kube.KusoEnvironment, error) {
	f.patched = true
	return &kube.KusoEnvironment{}, nil
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
		r.Put("/api/projects/{project}/services/{service}/env-vars/{name}", h.SetEnvVar)
		r.Post("/api/projects/{project}/services/{service}/env", h.SetEnv)
		r.Post("/api/projects/{project}/services/{service}/changes", h.ApplyServiceChanges)
		r.Put("/api/projects/{project}/services/{service}/envs/{env}/env-vars/{name}", h.SetEnvScopedVar)
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
		method string // default PATCH
		path   string // default svc
	}{
		{"editor changes command", editor, `{"command":["sh","-c","echo $SECRET"]}`, http.StatusForbidden, "", ""},
		{"editor sets image", editor, `{"image":{"repository":"busybox","tag":"1"}}`, http.StatusForbidden, "", ""},
		{"editor sets release hook", editor, `{"release":{"command":["printenv"]}}`, http.StatusForbidden, "", ""},
		{"editor sets static buildCmd", editor, `{"static":{"buildCmd":"printenv"}}`, http.StatusForbidden, "", ""},
		{"editor re-sends unchanged command", editor, `{"command":["./run"],"port":3000}`, http.StatusOK, "", ""},
		{"editor clears release hook", editor, `{"release":{"clear":true}}`, http.StatusOK, "", ""},
		{"editor changes port", editor, `{"port":3000}`, http.StatusOK, "", ""},
		{"admin changes command", admin, `{"command":["sh","-c","echo hi"]}`, http.StatusOK, "", ""},
		// SEC-1/SEC-2: env names that load or choose code are exec fields.
		{"editor sets NODE_OPTIONS", editor, `{"value":"--import=data:text/javascript,1","auto":true}`, http.StatusForbidden, http.MethodPut, svc + "/env-vars/NODE_OPTIONS"},
		{"editor sets NIXPACKS_START_CMD as secret", editor, `{"secretValue":"env"}`, http.StatusForbidden, http.MethodPut, svc + "/env-vars/NIXPACKS_START_CMD"},
		{"editor sets plain var", editor, `{"value":"x","auto":true}`, http.StatusOK, http.MethodPut, svc + "/env-vars/API_URL"},
		{"admin sets NODE_OPTIONS", admin, `{"value":"--import=x","auto":true}`, http.StatusOK, http.MethodPut, svc + "/env-vars/NODE_OPTIONS"},
		{"editor bulk set keeps NODE_OPTIONS", editor, `{"envVars":[{"name":"NODE_OPTIONS","value":"--max-old-space-size=512"},{"name":"A","value":"1"}]}`, http.StatusNoContent, http.MethodPost, svc + "/env"},
		{"editor bulk set adds LD_PRELOAD", editor, `{"envVars":[{"name":"LD_PRELOAD","value":"/x.so"}]}`, http.StatusForbidden, http.MethodPost, svc + "/env"},
		{"editor staged env sets BASH_ENV", editor, `{"envSet":[{"name":"BASH_ENV","value":"/tmp/x"}]}`, http.StatusForbidden, http.MethodPost, svc + "/changes"},
		{"editor staged patch changes command", editor, `{"patch":{"command":["printenv"]}}`, http.StatusForbidden, http.MethodPost, svc + "/changes"},
		{"editor staged plain var", editor, `{"envSet":[{"name":"A","value":"1"}]}`, http.StatusOK, http.MethodPost, svc + "/changes"},
		{"editor per-env JAVA_TOOL_OPTIONS", editor, `{"value":"-x"}`, http.StatusForbidden, http.MethodPut, svc + "/envs/staging/env-vars/JAVA_TOOL_OPTIONS"},
	}
	for _, c := range cases {
		method, path := c.method, c.path
		if method == "" {
			method, path = http.MethodPatch, svc
		}
		fake, rr := do(c.claims, method, path, c.body)
		if rr.Code != c.want {
			t.Errorf("%s: status %d (%s), want %d", c.name, rr.Code, rr.Body.String(), c.want)
		}
		if fake.patched != (c.want < 300) {
			t.Errorf("%s: PatchService called = %v", c.name, fake.patched)
		}
	}

	if fake, rr := do(editor, http.MethodPost, "/api/projects/p1/services",
		`{"name":"x","runtime":"image","image":{"repository":"busybox"},"command":["printenv"]}`); rr.Code != http.StatusForbidden || fake.added {
		t.Errorf("editor create image service: status %d added=%v, want 403 and no create", rr.Code, fake.added)
	}
}
