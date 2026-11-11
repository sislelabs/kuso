package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/drains"
	"kuso/server/internal/kube"
	"kuso/server/internal/projectsecrets"
)

// OPS-N7 / AH-4: these mutations used to leave no trace in GET /api/audit.
// Each must write exactly one row naming the key/target, never the value.

type auditRow struct{ action, severity, resource, message string }

func auditRowsFor(t *testing.T, d *db.DB, action string) []auditRow {
	t.Helper()
	rows, err := d.DB.Query(`SELECT action, severity, resource, message FROM "Audit" WHERE action = $1 ORDER BY id`, action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []auditRow
	for rows.Next() {
		var r auditRow
		if err := rows.Scan(&r.action, &r.severity, &r.resource, &r.message); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func auditDo(t *testing.T, h http.Handler, claims *auth.Claims, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(auth.WithClaimsForTest(context.Background(), claims))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func expectOneAuditRow(t *testing.T, d *db.DB, action, mustContain, mustNotContain string) {
	t.Helper()
	rows := auditRowsFor(t, d, action)
	if len(rows) != 1 {
		t.Fatalf("%s: %d audit rows, want 1: %+v", action, len(rows), rows)
	}
	if mustContain != "" && !strings.Contains(rows[0].message+rows[0].resource, mustContain) {
		t.Errorf("%s: row %+v does not name %q", action, rows[0], mustContain)
	}
	if mustNotContain != "" && strings.Contains(rows[0].message+rows[0].resource, mustNotContain) {
		t.Errorf("%s: row %+v leaks %q", action, rows[0], mustNotContain)
	}
}

var auditAdmin = &auth.Claims{UserID: "u1", Username: "admin",
	Permissions: []string{string(auth.PermSettingsAdmin), string(auth.PermUserWrite)}}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAudit_SharedSecretSetUnset(t *testing.T) {
	d := openTestDB(t)
	seedUserWithProjectRole(t, d, "ed", "alpha", db.ProjectRoleEditor)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRServices:     "KusoServiceList",
		})
	svc := projectsecrets.New(&kube.Client{Clientset: kubefake.NewSimpleClientset(), Dynamic: dyn}, "kuso")
	h := &ProjectSecretsHandler{Svc: svc, DB: d, Audit: &audit.Service{DB: d, Enabled: true}, Logger: quietLogger()}
	r := chi.NewRouter()
	h.Mount(r)
	ed := &auth.Claims{UserID: "ed"}

	if rr := auditDo(t, r, ed, http.MethodPut, "/api/projects/alpha/shared-secrets", `{"key":"DATABASE_URL","value":"postgres://u:hunter2@evil/db"}`); rr.Code != http.StatusOK {
		t.Fatalf("set: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "sharedsecret.set", "DATABASE_URL", "hunter2")

	if rr := auditDo(t, r, ed, http.MethodDelete, "/api/projects/alpha/shared-secrets/DATABASE_URL", ""); rr.Code != http.StatusOK {
		t.Fatalf("unset: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "sharedsecret.unset", "DATABASE_URL", "")
}

type auditMemDrains struct{ m map[string]drains.Drain }

func (s *auditMemDrains) List(context.Context) ([]drains.Drain, error) { return nil, nil }
func (s *auditMemDrains) Get(_ context.Context, id string) (*drains.Drain, error) {
	d, ok := s.m[id]
	if !ok {
		return nil, drains.ErrNotFound
	}
	return &d, nil
}
func (s *auditMemDrains) Put(_ context.Context, d *drains.Drain, _ string) error {
	if d.ID == "" {
		d.ID = "dr1"
	}
	s.m[d.ID] = *d
	return nil
}
func (s *auditMemDrains) Delete(_ context.Context, id string) error { delete(s.m, id); return nil }

func TestAudit_DrainCreateDelete(t *testing.T) {
	d := openTestDB(t)
	h := &DrainsHandler{Store: &auditMemDrains{m: map[string]drains.Drain{}}, Audit: &audit.Service{DB: d, Enabled: true}, Logger: quietLogger()}
	r := chi.NewRouter()
	h.Mount(r)

	body := `{"name":"exfil","type":"http","url":"https://collector.example.com/ingest?token=s3cr3t","secret":"hmac-key"}`
	if rr := auditDo(t, r, auditAdmin, http.MethodPost, "/api/drains", body); rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "drain.create", "collector.example.com", "s3cr3t")
	expectOneAuditRow(t, d, "drain.create", "", "hmac-key")

	if rr := auditDo(t, r, auditAdmin, http.MethodDelete, "/api/drains/dr1", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "drain.delete", "dr1", "")
}

func TestAudit_AlertRuleDeleteAndDisable(t *testing.T) {
	d := openTestDB(t)
	seedAlertRule(t, d, "r1", "", db.AlertKindHTTP5xxRate)
	seedAlertRule(t, d, "r2", "", db.AlertKindHTTP5xxRate)
	r := chi.NewRouter()
	(&AlertsHandler{DB: d, Audit: &audit.Service{DB: d, Enabled: true}, Logger: quietLogger()}).Mount(r)

	if rr := auditDo(t, r, alertAdmin, http.MethodDelete, "/api/alerts/r1", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "alert.delete", "r1", "")

	if rr := auditDo(t, r, alertAdmin, http.MethodPost, "/api/alerts/r2/disable", ""); rr.Code != http.StatusNoContent {
		t.Fatalf("disable: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "alert.disable", "r2", "")
}

func TestAudit_UserCreateAndTokenIssue(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.DB.Exec(`INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "instanceRole", "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', 'h', false, true, 'local', 'admin', NOW(), NOW())`); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	a := &audit.Service{DB: d, Enabled: true}
	(&UsersHandler{DB: d, Audit: a, Logger: quietLogger()}).Mount(r)
	issuer, err := auth.NewIssuer("test-secret-test-secret-test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	(&TokensAdminHandler{DB: d, Issuer: issuer, Audit: a, Logger: quietLogger()}).Mount(r)

	rr := auditDo(t, r, auditAdmin, http.MethodPost, "/api/users", `{"username":"newbie","email":"n@x.io","password":"correct-horse-battery"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "user.create", "newbie", "correct-horse-battery")

	var uid string
	if err := d.DB.QueryRow(`SELECT id FROM "User" WHERE username = 'newbie'`).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	rr = auditDo(t, r, auditAdmin, http.MethodPost, "/api/tokens/user/"+uid, `{"name":"ci-bot"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("issue token: %d %s", rr.Code, rr.Body.String())
	}
	tokenJWT := strings.SplitN(strings.SplitN(rr.Body.String(), `"token":"`, 2)[1], `"`, 2)[0]
	expectOneAuditRow(t, d, "token.issue", "ci-bot", tokenJWT)
}

// OPS-9: self-minted API tokens, groups and alert channels were silent.
func TestAudit_OwnTokenGroupAndAlertChannel(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.DB.Exec(`INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "instanceRole", "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', 'h', false, true, 'local', 'admin', NOW(), NOW())`); err != nil {
		t.Fatal(err)
	}
	issuer, err := auth.NewIssuer("test-secret-test-secret-test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a := &audit.Service{DB: d, Enabled: true}
	r := chi.NewRouter()
	(&AdminHandler{DB: d, Issuer: issuer, Audit: a, Logger: quietLogger()}).Mount(r)
	(&GroupsHandler{DB: d, Audit: a, Logger: quietLogger()}).Mount(r)
	(&NotificationsHandler{DB: d, Audit: a, Logger: quietLogger()}).Mount(r)

	rr := auditDo(t, r, auditAdmin, http.MethodPost, "/api/tokens/my", `{"name":"laptop"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("mint token: %d %s", rr.Code, rr.Body.String())
	}
	tokenJWT := strings.SplitN(strings.SplitN(rr.Body.String(), `"token":"`, 2)[1], `"`, 2)[0]
	expectOneAuditRow(t, d, "token.create", "laptop", tokenJWT)
	tokRow := auditRowsFor(t, d, "token.create")[0]
	if tokRow.severity != "critical" {
		t.Errorf("token.create severity = %q, want critical", tokRow.severity)
	}
	tokID := strings.TrimPrefix(tokRow.resource, "token/")
	if rr := auditDo(t, r, auditAdmin, http.MethodDelete, "/api/tokens/my/"+tokID, ""); rr.Code != http.StatusNoContent {
		t.Fatalf("revoke token: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "token.revoke", tokID, "")

	if rr := auditDo(t, r, auditAdmin, http.MethodPost, "/api/groups", `{"name":"oncall"}`); rr.Code != http.StatusCreated {
		t.Fatalf("create group: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "group.create", "oncall", "")

	rr = auditDo(t, r, auditAdmin, http.MethodPost, "/api/notifications",
		`{"name":"pager","type":"webhook","enabled":true,"config":{"url":"https://hooks.example.com/x","secret":"hmac-s3cret"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create channel: %d %s", rr.Code, rr.Body.String())
	}
	expectOneAuditRow(t, d, "notification.create", "pager", "hmac-s3cret")
}
