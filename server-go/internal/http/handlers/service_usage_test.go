package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
	"kuso/server/internal/podusage"
)

func TestServiceUsageHistory(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList",
	})
	for gvr, obj := range map[schema.GroupVersionResource][2]string{
		kube.GVRProjects: {"KusoProject", "shop"},
		kube.GVRServices: {"KusoService", "shop-web"},
	} {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(obj[0]))
		u.SetNamespace("kuso")
		u.SetName(obj[1])
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}

	store := podusage.NewStore()
	key := podusage.Key{Project: "shop", Service: "web", Env: "shop-web-production"}
	now := time.Now()
	store.Record(now.Add(-2*time.Hour), map[podusage.Key]podusage.Sample{key: {CPUMilli: 1, MemBytes: 1, Pods: 1}})
	store.Record(now.Add(-time.Minute), map[podusage.Key]podusage.Sample{key: {CPUMilli: 120, MemBytes: 64 << 20, Pods: 2}})

	h := &ServiceUsageHandler{Store: store, Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
	r := chi.NewRouter()
	h.Mount(r)
	admin := &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}
	do := func(path string, claims *auth.Claims) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if claims != nil {
			req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
		}
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	for _, tc := range []struct {
		path   string
		claims *auth.Claims
		want   int
	}{
		{"/api/projects/shop/services/web/usage/history?env=shop-web-production", nil, http.StatusUnauthorized},
		{"/api/projects/nosuch/services/web/usage/history?env=x", admin, http.StatusNotFound},
		{"/api/projects/shop/services/wbe/usage/history?env=x", admin, http.StatusNotFound},
		{"/api/projects/shop/services/web/usage/history", admin, http.StatusBadRequest},
		{"/api/projects/shop/services/web/usage/history?env=shop-web-production&range=bogus", admin, http.StatusBadRequest},
	} {
		if rr := do(tc.path, tc.claims); rr.Code != tc.want {
			t.Errorf("%s: status %d, want %d (%s)", tc.path, rr.Code, tc.want, rr.Body)
		}
	}

	// The FQ service name resolves to the same series as the short one.
	for _, svc := range []string{"web", "shop-web"} {
		rr := do("/api/projects/shop/services/"+svc+"/usage/history?env=shop-web-production&range=1h", admin)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", svc, rr.Code, rr.Body)
		}
		var body struct {
			StepSeconds int `json:"stepSeconds"`
			Samples     []struct {
				T        int64   `json:"t"`
				CPU      float64 `json:"cpuMillicores"`
				MemBytes int64   `json:"memBytes"`
				Pods     int     `json:"pods"`
			} `json:"samples"`
			Since *int64 `json:"since"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.StepSeconds != 30 || len(body.Samples) != 1 || body.Samples[0].CPU != 120 || body.Samples[0].Pods != 2 ||
			body.Since == nil || *body.Since != now.Add(-2*time.Hour).UnixMilli() {
			t.Errorf("%s: body = %s", svc, rr.Body)
		}
	}

	// 7d is capped to the 24h the store holds, not rejected.
	rr := do("/api/projects/shop/services/web/usage/history?env=shop-web-production&range=7d", admin)
	if rr.Code != http.StatusOK {
		t.Fatalf("7d: status %d", rr.Code)
	}
	var capped struct {
		StepSeconds int               `json:"stepSeconds"`
		Samples     []json.RawMessage `json:"samples"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &capped)
	if capped.StepSeconds != 300 || len(capped.Samples) != 2 {
		t.Errorf("7d: step %d, %d samples; want 300s buckets over 24h holding both samples", capped.StepSeconds, len(capped.Samples))
	}
}
