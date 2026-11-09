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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// recreatedServiceKube fakes project "shop" with service "shop-api" whose
// CR was created at born — i.e. a service re-created under a deleted
// one's name.
func recreatedServiceKube(t *testing.T, born time.Time) *kube.Client {
	t.Helper()
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
		u.SetCreationTimestamp(metav1.NewTime(born))
		if err := dyn.Tracker().Create(o.gvr, u, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	return &kube.Client{Dynamic: dyn}
}

func adminGet(t *testing.T, r http.Handler, path string, out any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(auth.WithClaimsForTest(req.Context(),
		&auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}}))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s: status=%d body=%s", path, rr.Code, rr.Body)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s: decode: %v", path, err)
	}
}

// A service created with a deleted service's name must start with empty
// error groups, revisions and audit trail — those tables are keyed by
// name only, so the reads clamp to the live CR's creation time.
func TestRecreatedService_DoesNotInheritHistory(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	born := time.Now().Add(-time.Hour).Truncate(time.Second)
	before, after := born.Add(-30*time.Minute), born.Add(10*time.Minute)
	kc := recreatedServiceKube(t, born)

	for _, e := range []db.ErrorEvent{
		{Project: "shop", Service: "shop-api", Env: "production", Fingerprint: "old", Message: "old boom", RawLine: "old boom", Ts: before},
		{Project: "shop", Service: "shop-api", Env: "production", Fingerprint: "new", Message: "new boom", RawLine: "new boom", Ts: after},
	} {
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := d.DB.ExecContext(ctx, `DELETE FROM "Revision" WHERE project = 'shop'`); err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{before, after} {
		if err := d.InsertRevision(ctx, db.Revision{Project: "shop", Kind: "service", Name: "api", Snapshot: json.RawMessage(`{}`), CreatedAt: at}); err != nil {
			t.Fatal(err)
		}
	}
	for _, at := range []time.Time{before, after} {
		if _, err := d.DB.ExecContext(ctx, `
INSERT INTO "Audit" (timestamp, severity, action, namespace, phase, app, pipeline, resource, message, "user", "createdAt", "updatedAt")
VALUES ($1, 'normal', 'update', 'kuso', 'production', 'api', 'shop', 'service', 'm', '1', $1, $1)`, at.UTC()); err != nil {
			t.Fatal(err)
		}
	}

	r := chi.NewRouter()
	(&ErrorsHandler{DB: d, Logger: slog.Default(), Kube: kc, Namespace: "kuso"}).Mount(r)
	(&ProjectsHandler{DB: d, Logger: slog.Default(), Kube: kc, Namespace: "kuso"}).Mount(r)
	(&AuditHandler{Svc: &audit.Service{DB: d, Enabled: true}, DB: d, Logger: slog.Default(), Kube: kc, Namespace: "kuso"}).Mount(r)

	var groups []db.ErrorGroup
	adminGet(t, r, "/api/projects/shop/services/api/errors?since=7d", &groups)
	if len(groups) != 1 || groups[0].Fingerprint != "new" {
		t.Errorf("error groups = %+v; want only the one seen after re-creation", groups)
	}
	var sum errorSummaryResponse
	adminGet(t, r, "/api/projects/shop/errors/summary?since=7d", &sum)
	if got := sum.Services["api"]; got.Groups != 1 || got.Occurrences != 1 {
		t.Errorf("summary api = %+v; want 1 group / 1 occurrence", got)
	}
	var det db.ErrorGroupDetail
	adminGet(t, r, "/api/projects/shop/services/api/errors/old?since=7d", &det)
	if len(det.Occurrences) != 0 {
		t.Errorf("old group detail leaked %d occurrences", len(det.Occurrences))
	}

	var revs []db.Revision
	adminGet(t, r, "/api/projects/shop/revisions/service/api", &revs)
	if len(revs) != 1 || revs[0].CreatedAt.Before(born) {
		t.Errorf("revisions = %+v; want only the one after re-creation", revs)
	}

	var trail struct {
		Audit []audit.Entry `json:"audit"`
		Count int           `json:"count"`
	}
	adminGet(t, r, "/api/audit/app/shop/production/api", &trail)
	if len(trail.Audit) != 1 || trail.Count != 1 {
		t.Errorf("audit = %d rows (count %d); want 1", len(trail.Audit), trail.Count)
	}
}
