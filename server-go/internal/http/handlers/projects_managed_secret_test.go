package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// managedSecretFakeSvc is a minimal ProjectsAPI for the read-enrichment
// wiring test. It embeds the interface (so the ~50 unused methods satisfy
// the type without boilerplate — any accidental call panics loudly) and
// overrides only the two methods GetService drives: the fetch and the
// managed-secret enrichment. The enrichment mirrors the real domain
// method's contract — it appends a name-only, Source=managed-secret entry
// for a key that lives in the managed secret but has no spec.envVars row.
type managedSecretFakeSvc struct {
	ProjectsAPI
	svc          *kube.KusoService
	orphanKey    string // key present in the managed secret, no spec.envVars row
	enrichCalled bool
}

func (f *managedSecretFakeSvc) GetService(_ context.Context, _, _ string) (*kube.KusoService, error) {
	// Hand back a fresh decode-equivalent copy so the handler's in-place
	// mutation (enrich + mask) can't poison the fixture.
	cp := *f.svc
	cp.Spec.EnvVars = append([]kube.KusoEnvVar(nil), f.svc.Spec.EnvVars...)
	return &cp, nil
}

func (f *managedSecretFakeSvc) EnrichServiceWithManagedSecretKeys(_ context.Context, _, _ string, svc *kube.KusoService) {
	f.enrichCalled = true
	if svc == nil || f.orphanKey == "" {
		return
	}
	// Skip if already represented (matches mergeManagedSecretKeys).
	for _, e := range svc.Spec.EnvVars {
		if e.Name == f.orphanKey {
			return
		}
	}
	svc.Spec.EnvVars = append(svc.Spec.EnvVars, kube.KusoEnvVar{
		Name:   f.orphanKey,
		Source: managedSecretSourceForTest,
	})
}

// managedSecretSourceForTest mirrors projects.managedSecretSource — kept
// as a local const so the handler test doesn't reach into the projects
// package's unexported identifier.
const managedSecretSourceForTest = "managed-secret"

func newManagedSecretFake() *managedSecretFakeSvc {
	return &managedSecretFakeSvc{
		svc: &kube.KusoService{
			Spec: kube.KusoServiceSpec{
				EnvVars: []kube.KusoEnvVar{
					{Name: "API_KEY", Value: "super-secret"},
				},
			},
		},
		orphanKey: "STRIPE_KEY", // lives only in the managed secret
	}
}

func getServiceResponse(t *testing.T, h *ProjectsHandler, claims *auth.Claims) kube.KusoService {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/projects/p1/services/web", nil)
	r = r.WithContext(auth.WithClaimsForTest(r.Context(), claims))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("project", "p1")
	rctx.URLParams.Add("service", "web")
	r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

	rr := httptest.NewRecorder()
	h.GetService(rr, r)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	var got kube.KusoService
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, rr.Body.String())
	}
	return got
}

func envVarsByName(vars []kube.KusoEnvVar) map[string]kube.KusoEnvVar {
	m := map[string]kube.KusoEnvVar{}
	for _, e := range vars {
		m[e.Name] = e
	}
	return m
}

// TestGetService_SurfacesManagedSecretKey_Admin proves the GetService read
// handler runs the managed-secret enrichment before serialization: an
// orphaned managed-secret key (present in the secret, no spec.envVars row)
// appears in the response, name-only and tagged. Admin path is DB-free
// (settings:admin bypasses both the access gate and the value mask), so it
// runs everywhere — no Postgres required.
func TestGetService_SurfacesManagedSecretKey_Admin(t *testing.T) {
	fake := newManagedSecretFake()
	h := &ProjectsHandler{Svc: fake, DB: nil, Logger: slog.Default()}

	got := getServiceResponse(t, h,
		&auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}})

	if !fake.enrichCalled {
		t.Fatal("enrichment was not invoked before serialization")
	}
	byName := envVarsByName(got.Spec.EnvVars)

	orphan, ok := byName["STRIPE_KEY"]
	if !ok {
		t.Fatalf("managed-secret key STRIPE_KEY not surfaced: %+v", got.Spec.EnvVars)
	}
	if orphan.Value != "" {
		t.Errorf("managed-secret key should be name-only, got value %q", orphan.Value)
	}
	if orphan.Source != managedSecretSourceForTest {
		t.Errorf("managed-secret key not tagged: source=%q", orphan.Source)
	}
	// Admin sees the real literal value.
	if api := byName["API_KEY"]; api.Value != "super-secret" {
		t.Errorf("admin API_KEY should be plaintext, got %q", api.Value)
	}
}

// TestGetService_SurfacesManagedSecretKey_MaskedForNonAdmin is the
// end-to-end non-admin path: an editor passes the viewer access gate but
// lacks secrets:read, so the enrichment still surfaces the orphaned key
// (name-only) while the literal secret value is masked. Requires the
// project-membership DB, so it skips when KUSO_TEST_PG_DSN is unset.
func TestGetService_SurfacesManagedSecretKey_MaskedForNonAdmin(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "u1", "p1", db.ProjectRoleEditor)

	fake := newManagedSecretFake()
	h := &ProjectsHandler{Svc: fake, DB: d, Logger: slog.Default()}

	got := getServiceResponse(t, h,
		&auth.Claims{UserID: "u1", Permissions: []string{}})

	if !fake.enrichCalled {
		t.Fatal("enrichment was not invoked before serialization")
	}
	byName := envVarsByName(got.Spec.EnvVars)

	// Orphaned managed-secret key still surfaces, name-only, tagged —
	// the mask leaves empty-value entries alone.
	orphan, ok := byName["STRIPE_KEY"]
	if !ok {
		t.Fatalf("managed-secret key STRIPE_KEY not surfaced: %+v", got.Spec.EnvVars)
	}
	if orphan.Value != "" {
		t.Errorf("managed-secret key should be name-only, got value %q", orphan.Value)
	}
	if orphan.Source != managedSecretSourceForTest {
		t.Errorf("managed-secret key not tagged: source=%q", orphan.Source)
	}
	// The literal secret value is masked for the non-admin editor.
	if api := byName["API_KEY"]; api.Value != envMaskSentinel {
		t.Errorf("API_KEY value not masked for non-admin: got %q want %q", api.Value, envMaskSentinel)
	}
}
