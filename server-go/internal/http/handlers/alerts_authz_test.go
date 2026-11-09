package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// Alert rules are open to project members: read on viewer, write on
// editor, scoped to the rule's project. Cluster-wide and node rules stay
// admin-only.

func alertsTestRouter(d *db.DB) chi.Router {
	r := chi.NewRouter()
	(&AlertsHandler{DB: d, Logger: slog.Default()}).Mount(r)
	return r
}

func alertsDo(t *testing.T, r chi.Router, claims *auth.Claims, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.WithClaimsForTest(req.Context(), claims))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func seedAlertRule(t *testing.T, d *db.DB, id, project, kind string) {
	t.Helper()
	if err := d.CreateAlertRule(context.Background(), db.AlertRule{
		ID: id, Name: id, Enabled: true, Kind: kind, Project: project,
		WindowSeconds: 300, Severity: "warn", ThrottleSeconds: 600,
	}); err != nil {
		t.Fatalf("seed rule %s: %v", id, err)
	}
}

var (
	alertAdmin  = &auth.Claims{UserID: "admin", Permissions: []string{string(auth.PermSettingsAdmin)}}
	alertMember = &auth.Claims{UserID: "member", Permissions: []string{}}
)

// member: editor on "mine", viewer on "peek", nothing on "theirs".
func seedAlertMember(t *testing.T, d *db.DB) {
	t.Helper()
	seedUserWithProjectRole(t, d, "member", "mine", db.ProjectRoleEditor)
	if _, err := d.AddProjectGrant(context.Background(), "peek", "member", "", db.ProjectRoleViewer); err != nil {
		t.Fatalf("seed viewer grant: %v", err)
	}
}

func TestAlerts_MemberCreate(t *testing.T) {
	d := openTestDB(t)
	seedAlertMember(t, d)
	r := alertsTestRouter(d)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"own project", `{"name":"x","kind":"http_5xx_rate","project":"mine","service":"web"}`, http.StatusCreated},
		{"unowned project", `{"name":"x","kind":"http_5xx_rate","project":"theirs"}`, http.StatusForbidden},
		{"viewer-only project", `{"name":"x","kind":"http_5xx_rate","project":"peek"}`, http.StatusForbidden},
		{"cluster-wide", `{"name":"x","kind":"http_5xx_rate"}`, http.StatusForbidden},
		{"cluster-wide log match", `{"name":"x","kind":"log_match","query":"panic"}`, http.StatusForbidden},
		// node rules ignore the project, so one tagged with the member's
		// project would still watch every node.
		{"node rule tagged with own project", `{"name":"x","kind":"node_cpu","project":"mine"}`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := alertsDo(t, r, alertMember, http.MethodPost, "/api/alerts", tc.body)
			if rr.Code != tc.want {
				t.Fatalf("status=%d want %d body=%s", rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestAlerts_MemberListOnlySeesReadableProjects(t *testing.T) {
	d := openTestDB(t)
	seedAlertMember(t, d)
	seedAlertRule(t, d, "r-mine", "mine", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-peek", "peek", db.AlertKindCertExpiry)
	seedAlertRule(t, d, "r-theirs", "theirs", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-cluster", "", db.AlertKindDNSMismatch)
	seedAlertRule(t, d, "r-node", "mine", db.AlertKindNodeDisk)
	r := alertsTestRouter(d)

	ids := func(claims *auth.Claims) string {
		rr := alertsDo(t, r, claims, http.MethodGet, "/api/alerts", "")
		if rr.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", rr.Code, rr.Body.String())
		}
		var out []db.AlertRule
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		var got []string
		for _, rule := range out {
			got = append(got, rule.ID)
		}
		return strings.Join(got, ",")
	}
	if got := ids(alertMember); got != "r-mine,r-peek" {
		t.Errorf("member sees %q, want r-mine,r-peek", got)
	}
	if got := ids(alertAdmin); got != "r-cluster,r-mine,r-node,r-peek,r-theirs" {
		t.Errorf("admin sees %q, want every rule", got)
	}
}

func TestAlerts_MemberMutations(t *testing.T) {
	d := openTestDB(t)
	seedAlertMember(t, d)
	seedAlertRule(t, d, "r-mine", "mine", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-peek", "peek", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-theirs", "theirs", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-cluster", "", db.AlertKindDNSMismatch)
	seedAlertRule(t, d, "r-node", "mine", db.AlertKindNodeDisk)
	r := alertsTestRouter(d)

	cases := []struct {
		id   string
		want int
	}{
		{"r-theirs", http.StatusNotFound},
		{"r-cluster", http.StatusNotFound},
		{"r-node", http.StatusNotFound},
		{"r-peek", http.StatusForbidden},
		{"r-missing", http.StatusNotFound},
	}
	for _, tc := range cases {
		for _, op := range []struct{ method, suffix string }{
			{http.MethodPost, "/disable"},
			{http.MethodPost, "/enable"},
			{http.MethodDelete, ""},
		} {
			rr := alertsDo(t, r, alertMember, op.method, "/api/alerts/"+tc.id+op.suffix, "")
			if rr.Code != tc.want {
				t.Errorf("%s %s%s: status=%d want %d", op.method, tc.id, op.suffix, rr.Code, tc.want)
			}
		}
	}
	rules, err := d.ListAlertRules(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		if !rule.Enabled {
			t.Errorf("rule %s was disabled by a member without editor access", rule.ID)
		}
	}
	if len(rules) != 5 {
		t.Errorf("rules left = %d, want 5 (a denied delete went through)", len(rules))
	}

	if rr := alertsDo(t, r, alertMember, http.MethodPost, "/api/alerts/r-mine/disable", ""); rr.Code != http.StatusNoContent {
		t.Errorf("editor disable own rule: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := alertsDo(t, r, alertMember, http.MethodDelete, "/api/alerts/r-mine", ""); rr.Code != http.StatusNoContent {
		t.Errorf("editor delete own rule: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAlerts_AdminUnchanged(t *testing.T) {
	d := openTestDB(t)
	seedAlertRule(t, d, "r-theirs", "theirs", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r-node", "", db.AlertKindNodeCPU)
	r := alertsTestRouter(d)

	for _, body := range []string{
		`{"name":"c","kind":"node_cpu"}`,
		`{"name":"d","kind":"dns_mismatch"}`,
		`{"name":"e","kind":"http_5xx_rate","project":"anything"}`,
	} {
		if rr := alertsDo(t, r, alertAdmin, http.MethodPost, "/api/alerts", body); rr.Code != http.StatusCreated {
			t.Errorf("admin create %s: status=%d body=%s", body, rr.Code, rr.Body.String())
		}
	}
	if rr := alertsDo(t, r, alertAdmin, http.MethodPost, "/api/alerts/r-theirs/disable", ""); rr.Code != http.StatusNoContent {
		t.Errorf("admin disable: status=%d", rr.Code)
	}
	if rr := alertsDo(t, r, alertAdmin, http.MethodDelete, "/api/alerts/r-node", ""); rr.Code != http.StatusNoContent {
		t.Errorf("admin delete: status=%d", rr.Code)
	}
	if rr := alertsDo(t, r, alertAdmin, http.MethodDelete, "/api/alerts/r-missing", ""); rr.Code != http.StatusNotFound {
		t.Errorf("admin delete missing: status=%d want 404", rr.Code)
	}
}

func TestAlerts_PendingUserSeesNothing(t *testing.T) {
	d := openTestDB(t)
	seedUserNoGroup(t, d, "pending")
	seedAlertRule(t, d, "r-a", "a", db.AlertKindHTTP5xxRate)
	r := alertsTestRouter(d)
	pending := &auth.Claims{UserID: "pending", Permissions: []string{}}
	rr := alertsDo(t, r, pending, http.MethodGet, "/api/alerts", "")
	if rr.Code != http.StatusOK || strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Errorf("pending list: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := alertsDo(t, r, pending, http.MethodPost, "/api/alerts", `{"name":"x","kind":"http_5xx_rate","project":"a"}`); rr.Code != http.StatusForbidden {
		t.Errorf("pending create: status=%d want 403", rr.Code)
	}
}

func TestAlerts_MemberUpdate(t *testing.T) {
	d := openTestDB(t)
	seedAlertMember(t, d)
	seedAlertRule(t, d, "own", "mine", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "peeked", "peek", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "hidden", "theirs", db.AlertKindHTTP5xxRate)
	r := alertsTestRouter(d)
	cases := []struct {
		name, id, body string
		want           int
	}{
		{"edit own rule", "own", `{"name":"renamed","kind":"http_5xx_rate","project":"mine","thresholdFloat":9}`, http.StatusOK},
		{"move own rule to an unowned project", "own", `{"name":"x","kind":"http_5xx_rate","project":"theirs"}`, http.StatusForbidden},
		{"make own rule cluster-wide", "own", `{"name":"x","kind":"http_5xx_rate"}`, http.StatusForbidden},
		{"viewer-only rule", "peeked", `{"name":"x","kind":"http_5xx_rate","project":"peek"}`, http.StatusForbidden},
		{"invisible rule", "hidden", `{"name":"x","kind":"http_5xx_rate","project":"mine"}`, http.StatusNotFound},
		{"bad body", "own", `{"name":"","kind":"http_5xx_rate","project":"mine"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := alertsDo(t, r, alertMember, http.MethodPut, "/api/alerts/"+tc.id, tc.body)
			if rr.Code != tc.want {
				t.Fatalf("PUT %s = %d (%s), want %d", tc.id, rr.Code, rr.Body.String(), tc.want)
			}
		})
	}
	got, err := d.GetAlertRule(context.Background(), "own")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "renamed" || got.ThresholdFloat == nil || *got.ThresholdFloat != 9 || got.Project != "mine" || !got.Enabled {
		t.Fatalf("stored rule = %+v", got)
	}
}
