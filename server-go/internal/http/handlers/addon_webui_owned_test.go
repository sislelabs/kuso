package handlers

// addon_webui_owned_test.go covers the cross-tenant ownership guard on
// the addon web-UI reverse proxy. The attack shape: project names
// overlap ("foo" vs "foo-bar"), so a foo-authorized viewer requesting
// GET /api/projects/foo/addons/foo-bar-pg/webui would — under the raw
// AddonFQN/CRName string mapping — resolve foo-bar's CR and be proxied
// straight into its web console (mailpit mail archive etc.). The
// handler must resolve through addons.Service.GetOwned so the fetched
// CR's spec.project is verified, and a mismatch must read exactly like
// a missing addon (404, no existence leak, no proxying).

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func webUIHandlerWithAddons(t *testing.T, addonCRs ...*kube.KusoAddon) *AddonWebUIHandler {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList",
	})
	for _, a := range addonCRs {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(a)
		if err != nil {
			t.Fatalf("encode addon: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
			t.Fatalf("seed addon: %v", err)
		}
	}
	return &AddonWebUIHandler{
		Svc:    addons.New(&kube.Client{Dynamic: dyn}, "kuso"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func webUIRequest(t *testing.T, h *AddonWebUIHandler, project, addon string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)
	// Admin claims pass requireProjectAccess for any project without a
	// DB — the property under test is the CR ownership re-check that
	// must hold even for a caller the role gate lets through.
	ctx := auth.WithClaimsForTest(context.Background(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	req := httptest.NewRequest(http.MethodGet,
		"/api/projects/"+project+"/addons/"+addon+"/webui", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestAddonWebUI_CrossProjectQualifiedNameIs404NotProxy(t *testing.T) {
	t.Parallel()
	h := webUIHandlerWithAddons(t, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "foo-bar-pg"},
		Spec: kube.KusoAddonSpec{
			Project: "foo-bar", Kind: "mailpit",
			WebUI: &kube.KusoAddonWebUI{Enabled: true},
		},
	})
	// Caller authorized on "foo" reaches for foo-bar's addon via the
	// prefix-colliding pre-qualified name. Must be the same 404 as a
	// missing addon — NOT a 502 (which would mean the proxy dial ran)
	// and NOT the webUI-disabled message (which would leak that the
	// CR exists and was inspected).
	rec := webUIRequest(t, h, "foo", "foo-bar-pg")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "webUI is not enabled") {
		t.Errorf("cross-tenant request reached the webUI gate: %s", rec.Body.String())
	}
}

func TestAddonWebUI_OwnerPassesOwnershipCheck(t *testing.T) {
	t.Parallel()
	// WebUI deliberately DISABLED: the owner's request must get past
	// the ownership check and fail at the webUI gate instead — the
	// distinct 404 message proves the flow reached the post-ownership
	// stage without attempting a network dial.
	h := webUIHandlerWithAddons(t, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "foo-bar-pg"},
		Spec:       kube.KusoAddonSpec{Project: "foo-bar", Kind: "mailpit"},
	})
	for _, name := range []string{"pg", "foo-bar-pg"} { // short + qualified forms
		rec := webUIRequest(t, h, "foo-bar", name)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("addon=%q: status = %d, want 404", name, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "webUI is not enabled") {
			t.Errorf("addon=%q: owner should reach the webUI gate, got: %s", name, rec.Body.String())
		}
	}
}

func TestAddonWebUI_MissingAddonIs404(t *testing.T) {
	t.Parallel()
	h := webUIHandlerWithAddons(t)
	rec := webUIRequest(t, h, "foo", "pg")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
