package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
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
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// The scanner stores ErrorEvent.service as the log line's service, which is
// the FQ `<project>-<service>` form. The CLI and web call the endpoint with
// the short name, so the handler must widen it or every lookup returns [].
func TestErrorsList_ShortServiceNameFindsFQNEvents(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.InsertErrorEvent(ctx, db.ErrorEvent{
		Project: "tickero", Service: "tickero-api", Env: "production", Pod: "p",
		Fingerprint: "fp1", Message: "ERROR boom", RawLine: "ERROR boom",
		Ts: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, svc := range []string{"api", "tickero-api"} {
		h := &ErrorsHandler{DB: d, Logger: slog.Default()}
		r := chi.NewRouter()
		h.Mount(r)
		req := httptest.NewRequest(http.MethodGet, "/api/projects/tickero/services/"+svc+"/errors", nil)
		req = req.WithContext(auth.WithClaimsForTest(req.Context(),
			&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%s", svc, rr.Code, rr.Body)
		}
		var groups []db.ErrorGroup
		if err := json.Unmarshal(rr.Body.Bytes(), &groups); err != nil {
			t.Fatalf("%s: decode: %v", svc, err)
		}
		if len(groups) != 1 || groups[0].Count != 1 {
			t.Errorf("service=%q: got %d groups %+v, want the one tickero-api event", svc, len(groups), groups)
		}
	}
}

func TestErrorsDetail_ReturnsOccurrences(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	if err := d.InsertErrorEvent(ctx, db.ErrorEvent{
		Project: "tickero", Service: "tickero-api", Env: "production", Pod: "p",
		Fingerprint: "fp-detail", Message: "ERROR boom", RawLine: "ERROR boom raw",
		Ts: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	h := &ErrorsHandler{DB: d, Logger: slog.Default()}
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/tickero/services/api/errors/fp-detail?since=1h", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body)
	}
	var det db.ErrorGroupDetail
	if err := json.Unmarshal(rr.Body.Bytes(), &det); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(det.Occurrences) != 1 || det.Occurrences[0].RawLine != "ERROR boom raw" || len(det.Buckets) != 1 {
		t.Errorf("detail = %+v", det)
	}

	bad := httptest.NewRequest(http.MethodGet, "/api/projects/tickero/services/api/errors/fp-detail?since=nope", nil)
	bad = bad.WithContext(req.Context())
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, bad)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("bad since: status=%d", rr.Code)
	}
}

func TestErrorsSummary_KeysByShortServiceName(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for _, e := range []db.ErrorEvent{
		{Project: "shop", Service: "shop-api", Env: "production", Fingerprint: "a", Message: "boom", Ts: time.Now().Add(-time.Minute)},
		{Project: "shop", Service: "shop-api", Env: "production", Fingerprint: "b", Message: "bang", Ts: time.Now().Add(-time.Minute)},
		{Project: "shop", Service: "shop-web", Env: "production", Fingerprint: "c", Message: "x", Ts: time.Now().Add(-2 * time.Hour)},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	h := &ErrorsHandler{DB: d, Logger: slog.Default()}
	r := chi.NewRouter()
	h.Mount(r)
	get := func(qs string) (int, errorSummaryResponse) {
		req := httptest.NewRequest(http.MethodGet, "/api/projects/shop/errors/summary"+qs, nil)
		req = req.WithContext(auth.WithClaimsForTest(req.Context(),
			&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		var out errorSummaryResponse
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
		}
		return rr.Code, out
	}

	code, out := get("")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if api := out.Services["api"]; api.Occurrences != 2 || api.Groups != 2 {
		t.Errorf("api = %+v; want 2 occurrences in 2 groups", api)
	}
	if _, ok := out.Services["web"]; !ok {
		t.Errorf("web missing from 24h summary: %+v", out.Services)
	}

	if _, out = get("?since=1h"); len(out.Services) != 1 {
		t.Errorf("since=1h: want only api, got %+v", out.Services)
	}
	if code, _ = get("?since=nonsense"); code != http.StatusBadRequest {
		t.Errorf("bad since: status=%d, want 400", code)
	}
}

// Errors of a deleted service must not count toward the project summary.
func TestErrorsSummary_DropsDeletedServices(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for _, e := range []db.ErrorEvent{
		{Project: "shop", Service: "shop-api", Fingerprint: "a", Message: "boom", Ts: time.Now().Add(-time.Minute)},
		{Project: "shop", Service: "shop-gone", Fingerprint: "b", Message: "bang", Ts: time.Now().Add(-time.Minute)},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList", kube.GVRServices: "KusoServiceList",
	})
	for _, o := range []struct {
		gvr        schema.GroupVersionResource
		kind, name string
	}{{kube.GVRProjects, "KusoProject", "shop"}, {kube.GVRServices, "KusoService", "shop-api"}} {
		u := &unstructured.Unstructured{Object: map[string]interface{}{}}
		u.SetGroupVersionKind(o.gvr.GroupVersion().WithKind(o.kind))
		u.SetName(o.name)
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(o.gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	h := &ErrorsHandler{DB: d, Logger: slog.Default(), Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
	r := chi.NewRouter()
	h.Mount(r)
	req := httptest.NewRequest(http.MethodGet, "/api/projects/shop/errors/summary", nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var out errorSummaryResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if _, ok := out.Services["gone"]; ok || out.Services["api"].Occurrences != 1 {
		t.Fatalf("services = %+v; want only api", out.Services)
	}
}
