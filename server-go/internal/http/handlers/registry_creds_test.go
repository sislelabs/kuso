package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
	"kuso/server/internal/registrycreds"
)

func registryCredsRouter(t *testing.T) http.Handler {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRServices: "KusoServiceList",
	})
	kc := &kube.Client{Clientset: fake.NewSimpleClientset(), Dynamic: dyn}
	h := &RegistryCredsHandler{
		Svc:    registrycreds.New(kc, "kuso"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	h.Mount(r)
	return r
}

func adminDo(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	ctx := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd).WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRegistryCreds_PasswordNeverEchoed(t *testing.T) {
	t.Parallel()
	h := registryCredsRouter(t)
	const pw = "ghp_supersecretvalue123"

	rec := adminDo(t, h, http.MethodPost, "/api/projects/shop/registry-credentials",
		`{"registry":"ghcr.io","username":"octo","password":"`+pw+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login status = %d body=%s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), pw) || strings.Contains(strings.ToLower(rec.Body.String()), "password") {
		t.Errorf("login response echoed the password: %s", rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"secretName":"shop-regcred-ghcr-io"`) {
		t.Errorf("login response missing secretName: %s", rec.Body)
	}

	rec = adminDo(t, h, http.MethodGet, "/api/projects/shop/registry-credentials", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), pw) || !strings.Contains(rec.Body.String(), `"username":"octo"`) {
		t.Errorf("list body = %s", rec.Body)
	}

	rec = adminDo(t, h, http.MethodDelete, "/api/projects/shop/registry-credentials/ghcr.io", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d body=%s", rec.Code, rec.Body)
	}
	rec = adminDo(t, h, http.MethodDelete, "/api/projects/shop/registry-credentials/ghcr.io", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("second logout status = %d, want 404", rec.Code)
	}
}

func TestRegistryCreds_BadInputIs400(t *testing.T) {
	t.Parallel()
	h := registryCredsRouter(t)
	for _, body := range []string{`{"registry":"ghcr.io","username":"u"}`, `{"registry":"bad host/x","username":"u","password":"p"}`, `not json`} {
		rec := adminDo(t, h, http.MethodPost, "/api/projects/shop/registry-credentials", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, rec.Code)
		}
	}
}

func TestRegistryCreds_RequiresAuth(t *testing.T) {
	t.Parallel()
	h := registryCredsRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/shop/registry-credentials", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list status = %d, want 401", rec.Code)
	}
}

// encodeURIComponent turns host:port into host%3Aport, and chi returns
// the raw segment, so a port-qualified credential could never be deleted.
func TestRegistryCreds_LogoutAcceptsEncodedHostPort(t *testing.T) {
	t.Parallel()
	h := registryCredsRouter(t)
	rec := adminDo(t, h, http.MethodPost, "/api/projects/shop/registry-credentials",
		`{"registry":"registry.example.com:5000","username":"octo","password":"pw"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("login status = %d body=%s", rec.Code, rec.Body)
	}
	rec = adminDo(t, h, http.MethodDelete, "/api/projects/shop/registry-credentials/registry.example.com%3A5000", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d body=%s", rec.Code, rec.Body)
	}
}
