package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

type stubServices map[string]bool

func (s stubServices) GetService(_ context.Context, project, service string) (*kube.KusoService, error) {
	if !s[project+"/"+service] {
		return nil, fmt.Errorf("%w: service %s/%s", projects.ErrNotFound, project, service)
	}
	return &kube.KusoService{}, nil
}

type stubAddons map[string]bool

func (s stubAddons) GetOwned(_ context.Context, project, name string) (*kube.KusoAddon, error) {
	if !s[project+"/"+name] {
		return nil, fmt.Errorf("%w: addon %s/%s", addons.ErrNotFound, project, name)
	}
	return &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: project + "-" + name, Namespace: "kuso"}}, nil
}

type stubLive struct{ service, addon *db.VulnImage }

func (s stubLive) ServiceImage(context.Context, string, string, string) (*db.VulnImage, error) {
	return s.service, nil
}
func (s stubLive) AddonImage(context.Context, string, *kube.KusoAddon) (*db.VulnImage, error) {
	return s.addon, nil
}
func (s stubLive) ProjectImages(context.Context, string) ([]db.VulnImage, error) {
	var out []db.VulnImage
	for _, im := range []*db.VulnImage{s.service, s.addon} {
		if im != nil {
			out = append(out, *im)
		}
	}
	return out, nil
}

const vulnDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func vulnsTestDB(t *testing.T) *db.DB {
	t.Helper()
	d := openTestDB(t)
	wipe := func() {
		_, _ = d.Exec(`TRUNCATE TABLE "VulnFinding", "VulnScan", "VulnImage", "VulnPolicy", "VulnIgnore", "VulnNotified" RESTART IDENTITY CASCADE`)
		_, _ = d.Exec(`DELETE FROM "Setting" WHERE key LIKE 'vulnscan.%'`)
	}
	wipe()
	t.Cleanup(wipe)
	return d
}

type vulnsRig struct {
	h          *VulnsHandler
	router     chi.Router
	invalidate int
}

func newVulnsRig(t *testing.T, live stubLive) *vulnsRig {
	d := vulnsTestDB(t)
	rig := &vulnsRig{}
	rig.h = &VulnsHandler{
		DB:               d,
		Services:         stubServices{"shop/web": true},
		Addons:           stubAddons{"shop/db": true},
		Live:             live,
		Sweep:            func(context.Context) (int, error) { return 3, nil },
		OnSettingsChange: func() { rig.invalidate++ },
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	rig.router = chi.NewRouter()
	rig.h.Mount(rig.router)
	return rig
}

var adminClaims = &auth.Claims{UserID: "u1", Username: "ivo", Permissions: []string{string(auth.PermSettingsAdmin)}}

func (r *vulnsRig) do(t *testing.T, claims *auth.Claims, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
	rec := httptest.NewRecorder()
	r.router.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestVulns_PolicyRoundTripAndValidation(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	rec, body := rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns/policy", "")
	if rec.Code != 200 || body["mode"] != "inherit" || body["effectiveMode"] != "report" {
		t.Fatalf("default policy: %d %v", rec.Code, body)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodPut, "/api/projects/shop/services/web/vulns/policy", `{"mode":"block"}`); rec.Code != 400 {
		t.Errorf("unknown mode: %d", rec.Code)
	}
	rec, body = rig.do(t, adminClaims, http.MethodPut, "/api/projects/shop/services/web/vulns/policy", `{"mode":"report"}`)
	if rec.Code != 200 || body["mode"] != "report" || body["effectiveMode"] != "report" {
		t.Fatalf("put report: %d %v", rec.Code, body)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/nope/vulns/policy", ""); rec.Code != 404 {
		t.Errorf("unknown service: %d", rec.Code)
	}
	outsider := &auth.Claims{UserID: "nobody"}
	if rec, _ := rig.do(t, outsider, http.MethodPut, "/api/projects/shop/services/web/vulns/policy", `{"mode":"off"}`); rec.Code != 403 {
		t.Errorf("non-member: %d", rec.Code)
	}
}

func TestVulns_IgnoresCRUD(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	base := "/api/projects/shop/services/web/vulns/ignores"
	for _, bad := range []string{
		`{"vulnId":"not a cve!","reason":"x"}`,
		`{"vulnId":"CVE-2024-1","reason":"  "}`,
		`{"vulnId":"CVE-2024-1","reason":"x","expiresInDays":0}`,
		`{"vulnId":"CVE-2024-1","reason":"x","expiresInDays":366}`,
	} {
		if rec, _ := rig.do(t, adminClaims, http.MethodPost, base, bad); rec.Code != 400 {
			t.Errorf("%s → %d, want 400", bad, rec.Code)
		}
	}
	rec, body := rig.do(t, adminClaims, http.MethodPost, base, `{"vulnId":"CVE-2024-1","reason":"not reachable","expiresInDays":30}`)
	if rec.Code != 201 || body["scope"] != "service" || body["project"] != "shop" || body["service"] != "web" ||
		body["createdBy"] != "ivo" || body["expired"] != false || body["expiresAt"] == nil {
		t.Fatalf("create: %d %v", rec.Code, body)
	}
	id := int64(body["id"].(float64))

	listRec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, base, nil)
	rig.router.ServeHTTP(listRec, req.WithContext(auth.WithClaimsForTest(req.Context(), adminClaims)))
	var list []map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list: %s", listRec.Body)
	}

	// An addon route can't delete a service ignore by id.
	if rec, _ := rig.do(t, adminClaims, http.MethodDelete, fmt.Sprintf("/api/projects/shop/addons/db/vulns/ignores/%d", id), ""); rec.Code != 404 {
		t.Errorf("cross-owner delete: %d, want 404", rec.Code)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodDelete, fmt.Sprintf("%s/%d", base, id), ""); rec.Code != 204 {
		t.Errorf("delete: %d", rec.Code)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/addons/other/vulns/ignores", ""); rec.Code != 404 {
		t.Errorf("unowned addon: %d, want 404", rec.Code)
	}
}

// SEC-5: under enforce an ignore lifts a deploy block, so an editor must
// not be able to add one; outside enforce they still can.
func TestVulns_EditorIgnoreBlockedUnderEnforce(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	ctx := context.Background()
	seedUserWithProjectRole(t, rig.h.DB, "ed-vuln", "shop", db.ProjectRoleEditor)
	editor := &auth.Claims{UserID: "ed-vuln", Username: "ed"}
	base := "/api/projects/shop/services/web/vulns/ignores"

	if rec, _ := rig.do(t, editor, http.MethodPost, base, `{"vulnId":"CVE-2024-1","reason":"x"}`); rec.Code != 201 {
		t.Fatalf("editor ignore under report: %d, want 201", rec.Code)
	}
	st := db.DefaultVulnScanSettings()
	st.Mode = "enforce"
	if err := rig.h.DB.SetVulnScanSettings(ctx, st, "test"); err != nil {
		t.Fatal(err)
	}
	if rec, _ := rig.do(t, editor, http.MethodPost, base, `{"vulnId":"CVE-2024-2","reason":"x"}`); rec.Code != 403 {
		t.Fatalf("editor ignore under enforce: %d, want 403", rec.Code)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodPost, base, `{"vulnId":"CVE-2024-2","reason":"x"}`); rec.Code != 201 {
		t.Fatalf("admin ignore under enforce: %d, want 201", rec.Code)
	}
}

func TestVulns_ServiceShowAndScan(t *testing.T) {
	im := &db.VulnImage{Digest: vulnDigest, ImageRef: "r/web@" + vulnDigest, OwnerKind: db.VulnOwnerService,
		Project: "shop", Service: "web", Env: "production", Namespace: "kuso", Workload: "shop-web-production"}
	rig := newVulnsRig(t, stubLive{service: im})
	ctx := context.Background()
	d := rig.h.DB

	rec, body := rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns", "")
	if rec.Code != 200 || body["image"] == nil || body["image"].(map[string]any)["scan"] != nil || body["policy"] != "inherit" {
		t.Fatalf("unscanned image: %d %v", rec.Code, body)
	}

	rec, body = rig.do(t, adminClaims, http.MethodPost, "/api/projects/shop/services/web/vulns/scan", "")
	if rec.Code != 202 || body["scanId"] == nil {
		t.Fatalf("scan: %d %v", rec.Code, body)
	}
	if inv, _ := d.ListVulnImages(ctx, db.VulnImageFilter{Project: "shop"}); len(inv) != 1 {
		t.Errorf("a manual scan records the sighting: %+v", inv)
	}
	sc, _ := d.ClaimNextVulnScan(ctx)
	if sc == nil || sc.Reason != db.VulnReasonManual {
		t.Fatalf("queued scan = %+v", sc)
	}
	if err := d.CompleteVulnScan(ctx, sc.ID, db.VulnScanResult{Findings: []db.VulnFinding{
		{VulnID: "CVE-C", Severity: "CRITICAL", FixedVersion: "2", PkgName: "openssl"},
		{VulnID: "CVE-H", Severity: "HIGH", PkgName: "curl"},
		{VulnID: "CVE-L", Severity: "LOW", PkgName: "x"},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateVulnIgnore(ctx, db.VulnIgnore{Scope: "service", Project: "shop", Service: "web", VulnID: "CVE-C", Reason: "r"}); err != nil {
		t.Fatal(err)
	}

	rec, body = rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns?severity=CRITICAL,HIGH", "")
	if rec.Code != 200 {
		t.Fatalf("show: %d %s", rec.Code, rec.Body)
	}
	scan := body["image"].(map[string]any)["scan"].(map[string]any)
	counts := scan["counts"].(map[string]any)
	if scan["status"] != "succeeded" || counts["critical"] != 0.0 || counts["high"] != 1.0 || counts["low"] != 1.0 || scan["ignoredCount"] != 1.0 {
		t.Errorf("summary must exclude ignored findings: %v", scan)
	}
	fs := body["findings"].([]any)
	if len(fs) != 2 || fs[0].(map[string]any)["ignored"] != true || fs[0].(map[string]any)["ignoreId"] == nil {
		t.Errorf("findings = %v", fs)
	}
	rec, body = rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns?includeIgnored=false", "")
	if len(body["findings"].([]any)) != 2 {
		t.Errorf("hide ignored: %v", body["findings"])
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns?severity=SEVERE", ""); rec.Code != 400 {
		t.Errorf("bad severity: %d", rec.Code)
	}

	rec, body = rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/vulns", "")
	if rec.Code != 200 || body["mode"] != "report" || len(body["images"].([]any)) != 1 {
		t.Errorf("project list: %d %v", rec.Code, body)
	}
}

func TestVulns_ScanWithoutLiveImageIs409(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	rec, body := rig.do(t, adminClaims, http.MethodPost, "/api/projects/shop/addons/db/vulns/scan", "")
	if rec.Code != 409 || !strings.Contains(fmt.Sprint(body["error"]), "nothing to scan") {
		t.Errorf("no live image: %d %v", rec.Code, body)
	}
}

func TestVulns_AdminSettings(t *testing.T) {
	rig := newVulnsRig(t, stubLive{})
	viewer := &auth.Claims{UserID: "u2"}
	if rec, _ := rig.do(t, viewer, http.MethodGet, "/api/admin/settings/vulnscan", ""); rec.Code != 403 {
		t.Errorf("non-admin settings: %d", rec.Code)
	}
	rec, body := rig.do(t, adminClaims, http.MethodGet, "/api/admin/settings/vulnscan", "")
	if rec.Code != 200 || body["mode"] != "report" || body["nightlyHourUTC"] != 3.0 || body["notifyMinSeverity"] != "high" {
		t.Fatalf("defaults: %v", body)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodPut, "/api/admin/settings/vulnscan", `{"mode":"report","scanAddons":true,"scanPlatform":true,"nightlyHourUTC":24,"notifyMinSeverity":"high"}`); rec.Code != 400 {
		t.Errorf("hour 24: %d", rec.Code)
	}
	rec, body = rig.do(t, adminClaims, http.MethodPut, "/api/admin/settings/vulnscan", `{"mode":"off","scanAddons":false,"scanPlatform":true,"nightlyHourUTC":0,"notifyMinSeverity":"none"}`)
	if rec.Code != 200 || body["mode"] != "off" || body["scanAddons"] != false || rig.invalidate != 1 {
		t.Fatalf("put: %d %v invalidate=%d", rec.Code, body, rig.invalidate)
	}
	rec, body = rig.do(t, adminClaims, http.MethodGet, "/api/projects/shop/services/web/vulns/policy", "")
	if body["effectiveMode"] != "off" {
		t.Errorf("instance off wins: %v", body)
	}
	if rec, body := rig.do(t, adminClaims, http.MethodPost, "/api/admin/vulns/scan-all", ""); rec.Code != 202 || body["queued"] != 3.0 {
		t.Errorf("scan-all: %d %v", rec.Code, body)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodGet, "/api/admin/vulns/platform/findings", ""); rec.Code != 400 {
		t.Errorf("findings without digest: %d", rec.Code)
	}
	if rec, _ := rig.do(t, adminClaims, http.MethodGet, "/api/admin/vulns/scans/999", ""); rec.Code != 404 {
		t.Errorf("unknown scan: %d", rec.Code)
	}
}
