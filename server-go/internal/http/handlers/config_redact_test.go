package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/config"
	"kuso/server/internal/kube"
)

func configHandlerWithSpec(t *testing.T, spec map[string]any) *ConfigHandler {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRKuso: "KusoList"})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.Kuso{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso", Namespace: "kuso"}, Spec: spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRKuso.GroupVersion().WithKind("Kuso"))
	if err := dyn.Tracker().Create(kube.GVRKuso, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	cfg := config.New(&kube.Client{Clientset: k8sfake.NewSimpleClientset(), Dynamic: dyn}, "kuso")
	if err := cfg.Reload(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &ConfigHandler{Cfg: cfg}
}

// /api/config and /api/config/registry sat outside the admin group and
// echoed the raw Kuso CR spec — registry credentials and the OAuth2
// client secret included — to any JWT, pending users too.
func TestConfig_NonAdminDoesNotSeeSecrets(t *testing.T) {
	t.Parallel()
	h := configHandlerWithSpec(t, map[string]any{
		"registry": map[string]any{"enabled": true, "host": "reg.example.com", "password": "reg-s3cret"},
		"kuso":     map[string]any{"auth": map[string]any{"oauth2": map[string]any{"clientSecret": "oauth-s3cret"}}},
	})
	get := func(fn http.HandlerFunc, perms []string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(auth.WithClaimsForTest(req.Context(), &auth.Claims{UserID: "u", Permissions: perms}))
		rr := httptest.NewRecorder()
		fn(rr, req)
		return rr.Body.String()
	}
	for name, fn := range map[string]http.HandlerFunc{"config": h.GetSettings, "registry": h.Registry} {
		if body := get(fn, nil); strings.Contains(body, "s3cret") {
			t.Errorf("%s leaks a secret to a non-admin: %s", name, body)
		}
	}
	if body := get(h.Registry, nil); !strings.Contains(body, `"enabled":true`) || !strings.Contains(body, "reg.example.com") {
		t.Errorf("registry should keep allowlisted fields for non-admins: %s", body)
	}
	admin := []string{string(auth.PermSettingsAdmin)}
	if body := get(h.GetSettings, admin); !strings.Contains(body, "oauth-s3cret") {
		t.Errorf("admin should see the full settings: %s", body)
	}
}
