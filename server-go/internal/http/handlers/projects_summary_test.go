package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// summaryFakeSvc is a minimal ProjectsAPI for the batched dashboard
// endpoint. Embeds the interface (unused methods panic loudly if hit)
// and overrides List + Describe + the two enrichment hooks the Summary
// handler drives. Describe hands back fresh copies per call so the
// handler's in-place mask/redact can't poison the fixtures.
type summaryFakeSvc struct {
	ProjectsAPI
	projects  []kube.KusoProject
	describes map[string]*projects.DescribeResponse
	// failDescribe simulates one broken project (kube blip) — the
	// handler must degrade that card, not the whole response.
	failDescribe map[string]bool
	enrichedSvcs int
	enrichedEnvs int
}

func (f *summaryFakeSvc) List(context.Context) ([]kube.KusoProject, error) {
	return append([]kube.KusoProject(nil), f.projects...), nil
}

func (f *summaryFakeSvc) Describe(_ context.Context, name string) (*projects.DescribeResponse, error) {
	if f.failDescribe[name] {
		return nil, errors.New("kube unavailable")
	}
	d, ok := f.describes[name]
	if !ok {
		return nil, errors.New("no fixture for " + name)
	}
	cp := &projects.DescribeResponse{Project: &kube.KusoProject{}}
	*cp.Project = *d.Project
	cp.Services = make([]kube.KusoService, len(d.Services))
	for i := range d.Services {
		cp.Services[i] = d.Services[i]
		cp.Services[i].Spec.EnvVars = append([]kube.KusoEnvVar(nil), d.Services[i].Spec.EnvVars...)
	}
	cp.Environments = make([]kube.KusoEnvironment, len(d.Environments))
	copy(cp.Environments, d.Environments)
	return cp, nil
}

func (f *summaryFakeSvc) EnrichServiceWithManagedSecretKeys(_ context.Context, _, _ string, _ *kube.KusoService) {
	f.enrichedSvcs++
}

func (f *summaryFakeSvc) EnrichEnvWithManagedSecretKeys(_ context.Context, _ string, _ *kube.KusoEnvironment) {
	f.enrichedEnvs++
}

func newSummaryFake() *summaryFakeSvc {
	mkProject := func(name string) kube.KusoProject {
		return kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}}
	}
	mkDescribe := func(project string) *projects.DescribeResponse {
		p := mkProject(project)
		return &projects.DescribeResponse{
			Project: &p,
			Services: []kube.KusoService{{
				ObjectMeta: metav1.ObjectMeta{Name: project + "-web"},
				Spec: kube.KusoServiceSpec{
					EnvVars: []kube.KusoEnvVar{{Name: "API_KEY", Value: "super-secret"}},
				},
			}},
			Environments: []kube.KusoEnvironment{{
				ObjectMeta: metav1.ObjectMeta{
					Name:   project + "-web",
					Labels: map[string]string{kube.LabelEnv: "production"},
				},
				Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-web"},
			}},
		}
	}
	return &summaryFakeSvc{
		projects: []kube.KusoProject{mkProject("p1"), mkProject("p2")},
		describes: map[string]*projects.DescribeResponse{
			"p1": mkDescribe("p1"),
			"p2": mkDescribe("p2"),
		},
		failDescribe: map[string]bool{},
	}
}

func summaryResponse(t *testing.T, h *ProjectsHandler, claims *auth.Claims) []projectSummaryItem {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/projects/summary", nil)
	r = r.WithContext(auth.WithClaimsForTest(r.Context(), claims))
	rr := httptest.NewRecorder()
	h.Summary(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var got []projectSummaryItem
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
	}
	return got
}

// TestProjectsSummary_AdminShape proves the batched endpoint returns one
// item per project with the describe-shaped payload + metrics rollup,
// runs the enrichment before serialization, and (admin path, DB-free)
// leaves env values plaintext. No Kube wired → metrics fall through to
// the zeros/"—" state with Envs still counted from the describe.
func TestProjectsSummary_AdminShape(t *testing.T) {
	fake := newSummaryFake()
	h := &ProjectsHandler{Svc: fake, DB: nil, Logger: slog.Default(), Namespace: "kuso"}

	got := summaryResponse(t, h,
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}})

	if len(got) != 2 {
		t.Fatalf("items=%d want 2", len(got))
	}
	byName := map[string]projectSummaryItem{}
	for _, it := range got {
		if it.Project == nil {
			t.Fatal("item missing project")
		}
		byName[it.Project.Name] = it
	}
	p1, ok := byName["p1"]
	if !ok {
		t.Fatalf("p1 missing from summary: %+v", got)
	}
	if len(p1.Services) != 1 || len(p1.Environments) != 1 {
		t.Fatalf("p1 shape: services=%d envs=%d, want 1/1", len(p1.Services), len(p1.Environments))
	}
	// Admin sees the plaintext value — same gate as Describe.
	if v := p1.Services[0].Spec.EnvVars[0].Value; v != "super-secret" {
		t.Errorf("admin env value = %q, want plaintext", v)
	}
	// Metrics mirror projectMetricsResponse: project echoed, production
	// env-group counted even when metrics-server data is unavailable.
	if p1.Metrics.Project != "p1" || p1.Metrics.Envs != 1 {
		t.Errorf("metrics = %+v, want project=p1 envs=1", p1.Metrics)
	}
	if p1.Metrics.Pods != 0 || p1.Metrics.CPUm != 0 {
		t.Errorf("no kube wired: metrics should be zeros, got %+v", p1.Metrics)
	}
	if fake.enrichedSvcs == 0 || fake.enrichedEnvs == 0 {
		t.Error("managed-secret enrichment not invoked before serialization")
	}
}

// TestProjectsSummary_NonAdminMasked: a caller without secrets:read gets
// the same items with env values masked — the batched endpoint must not
// leak what per-project Describe masks. Nil DB + non-admin exercises the
// fail-closed mask gate (callerCanReadSecrets → false).
func TestProjectsSummary_NonAdminMasked(t *testing.T) {
	fake := newSummaryFake()
	h := &ProjectsHandler{Svc: fake, DB: nil, Logger: slog.Default(), Namespace: "kuso"}

	got := summaryResponse(t, h, &auth.Claims{UserID: "viewer", Permissions: []string{}})

	for _, it := range got {
		for _, svc := range it.Services {
			for _, ev := range svc.Spec.EnvVars {
				if ev.Value != envMaskSentinel {
					t.Errorf("project %s: env value %q not masked for non-admin", it.Project.Name, ev.Value)
				}
			}
		}
	}
}

// TestProjectsSummary_DegradesBrokenProject: one project's describe
// failing (kube blip, half-deleted project) must yield a name-only item
// for that card, not blank the whole dashboard or 500.
func TestProjectsSummary_DegradesBrokenProject(t *testing.T) {
	fake := newSummaryFake()
	fake.failDescribe["p2"] = true
	h := &ProjectsHandler{Svc: fake, DB: nil, Logger: slog.Default(), Namespace: "kuso"}

	got := summaryResponse(t, h,
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}})

	if len(got) != 2 {
		t.Fatalf("items=%d want 2 (degraded card must still appear)", len(got))
	}
	for _, it := range got {
		if it.Project.Name == "p2" {
			if len(it.Services) != 0 || len(it.Environments) != 0 {
				t.Errorf("broken project should be name-only, got %+v", it)
			}
		}
		if it.Project.Name == "p1" && len(it.Services) != 1 {
			t.Errorf("healthy project degraded alongside the broken one: %+v", it)
		}
	}
}

// TestProjectsSummary_TenancyFilter is the DB-backed authz test (skips
// without KUSO_TEST_PG_DSN): a non-admin with a grant on p1 only must
// receive p1 only — the batched endpoint enforces the same tenancy
// filter as GET /api/projects.
func TestProjectsSummary_TenancyFilter(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleViewer)

	fake := newSummaryFake()
	h := &ProjectsHandler{Svc: fake, DB: d, Logger: slog.Default(), Namespace: "kuso"}

	got := summaryResponse(t, h, &auth.Claims{UserID: "u1", Permissions: []string{}})

	if len(got) != 1 || got[0].Project.Name != "p1" {
		names := make([]string, len(got))
		for i := range got {
			names[i] = got[i].Project.Name
		}
		t.Fatalf("non-admin summary = %v, want exactly [p1]", names)
	}
	// Viewer without secrets:read: values masked even through the DB path.
	if v := got[0].Services[0].Spec.EnvVars[0].Value; v != envMaskSentinel {
		t.Errorf("viewer env value = %q, want masked", v)
	}
}
