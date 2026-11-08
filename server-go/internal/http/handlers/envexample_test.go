package handlers

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

type envExampleRemote struct{ reads int }

func (r *envExampleRemote) HeadSHA(context.Context, string, string, string) (string, error) {
	return "", nil
}

func (r *envExampleRemote) ReadFile(_ context.Context, _, _, path, _ string) (string, error) {
	r.reads++
	if path == ".env.example" {
		return "STRIPE_KEY=sk_test_example\n", nil
	}
	return "", nil
}

func envExampleHandler(t *testing.T) (*BuildsHandler, *envExampleRemote) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList",
	})
	seed := func(gvr schema.GroupVersionResource, kind string, obj any) {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatal(err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	seed(kube.GVRProjects, "KusoProject", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/o/r", DefaultBranch: "main"}},
	})
	seed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api", Namespace: "kuso"},
		Spec:       kube.KusoServiceSpec{Project: "alpha", Repo: &kube.KusoRepoRef{URL: "https://github.com/o/r"}},
	})
	remote := &envExampleRemote{}
	svc := &builds.Service{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(), Dynamic: dyn}, Namespace: "kuso", Remote: remote}
	return &BuildsHandler{Svc: svc, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}, remote
}

func getEnvExample(h *BuildsHandler, ctx context.Context) *httptest.ResponseRecorder {
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/alpha/services/api/env-example", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

// The example file is repo content: only callers who can read the
// project's services may fetch it, and the repo isn't touched otherwise.
func TestEnvExample_RequiresProjectAccess(t *testing.T) {
	t.Parallel()
	h, remote := envExampleHandler(t)
	// No grant on alpha and no DB → the project gate fails closed.
	outsider := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u2", Permissions: []string{}})
	if rec := getEnvExample(h, outsider); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider got %d, want 403: %s", rec.Code, rec.Body)
	}
	if rec := getEnvExample(h, context.Background()); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous got %d, want 401", rec.Code)
	}
	if remote.reads != 0 {
		t.Errorf("repo read %d times for refused callers", remote.reads)
	}
}

func TestEnvExample_ReturnsEntries(t *testing.T) {
	t.Parallel()
	h, _ := envExampleHandler(t)
	admin := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	rec := getEnvExample(h, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var out builds.EnvExample
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.File != ".env.example" || len(out.Entries) != 1 || out.Entries[0].Key != "STRIPE_KEY" {
		t.Errorf("got %+v", out)
	}
}
