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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/addons"
	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

func TestEnablePublicTCP_RestrictionBody(t *testing.T) {
	t.Setenv("KUSO_TCP_PROXY_PORTS", "30000-30010")
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList",
	})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "shop-pg", Namespace: "kuso"},
		Spec:       kube.KusoAddonSpec{Project: "shop", Kind: "postgres"},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	h := &AddonsHandler{
		Svc:    addons.New(&kube.Client{Dynamic: dyn}, "kuso"),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	h.Mount(r)
	post := func(body string) *httptest.ResponseRecorder {
		ctx := auth.WithClaimsForTest(context.Background(),
			&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
		req := httptest.NewRequest(http.MethodPost, "/api/projects/shop/addons/pg/public-tcp",
			strings.NewReader(body)).WithContext(ctx)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	for body, want := range map[string]string{
		`{"sourceRanges":["office"]}`: `office\" is not a CIDR`,
		`{"maxConnections":-3}`:       "maxConnections must be between",
		`{"sourceRanges":`:            "invalid JSON body",
	} {
		rec := post(body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %d %s, want 400 containing %q", body, rec.Code, rec.Body.String(), want)
		}
	}

	rec := post(`{"sourceRanges":["198.51.100.7/24"],"maxConnections":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid body: %d %s", rec.Code, rec.Body.String())
	}
	if got, want := strings.TrimSpace(rec.Body.String()), `{"maxConnections":8,"port":30000,"sourceRanges":["198.51.100.0/24"]}`; got != want {
		t.Errorf("response = %s, want %s", got, want)
	}
}
