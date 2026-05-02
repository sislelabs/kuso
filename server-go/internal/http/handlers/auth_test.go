package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	httpsrv "kuso/server/internal/http"
)

func newTestServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	iss, err := auth.NewIssuer("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	r := httpsrv.NewRouter(httpsrv.Deps{
		DB:     d,
		Issuer: iss,
		Logger: slog.Default(),
	})
	return r, d, iss
}

func seedAdmin(t *testing.T, d *db.DB, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password, 4)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	now := time.Now().UTC()
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt") VALUES ('r1', 'admin', '', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", "roleId", provider, "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', ?, 0, 1, 'r1', 'local', ?, ?)`, hash, now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "Permission" (id, resource, action, "createdAt", "updatedAt") VALUES ('p1', 'app', 'read', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed perm: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "_PermissionToRole" ("A", "B") VALUES ('p1', 'r1')`); err != nil {
		t.Fatalf("seed pivot: %v", err)
	}
}

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	r, d, iss := newTestServer(t)
	seedAdmin(t, d, "hunter2")

	body := strings.NewReader(`{"username":"admin","password":"hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%q", rr.Code, rr.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AccessToken == "" {
		t.Fatal("empty access_token")
	}
	// Issued token must round-trip and carry the seeded permission.
	claims, err := iss.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "u1" || claims.Role != "admin" || claims.Strategy != "local" {
		t.Errorf("claims: %+v", claims)
	}
	if len(claims.Permissions) != 1 || claims.Permissions[0] != "app:read" {
		t.Errorf("permissions: %+v", claims.Permissions)
	}
}

func TestLogin_BadPassword(t *testing.T) {
	t.Parallel()
	r, d, _ := newTestServer(t)
	seedAdmin(t, d, "hunter2")

	body := strings.NewReader(`{"username":"admin","password":"wrong"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestLogin_UnknownUser(t *testing.T) {
	t.Parallel()
	r, _, _ := newTestServer(t)
	body := strings.NewReader(`{"username":"ghost","password":"x"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestLogin_BadRequestBody(t *testing.T) {
	t.Parallel()
	r, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("{not-json"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestSession_AfterLogin(t *testing.T) {
	t.Parallel()
	r, d, _ := newTestServer(t)
	seedAdmin(t, d, "hunter2")

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"hunter2"}`))
	loginRR := httptest.NewRecorder()
	r.ServeHTTP(loginRR, loginReq)
	if loginRR.Code != http.StatusOK {
		t.Fatalf("login: %d", loginRR.Code)
	}
	var lr struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(loginRR.Body).Decode(&lr)

	sessReq := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	sessReq.Header.Set("Authorization", "Bearer "+lr.AccessToken)
	sessRR := httptest.NewRecorder()
	r.ServeHTTP(sessRR, sessReq)
	if sessRR.Code != http.StatusOK {
		t.Fatalf("session: %d body=%q", sessRR.Code, sessRR.Body.String())
	}
	var s map[string]any
	_ = json.NewDecoder(sessRR.Body).Decode(&s)
	if s["isAuthenticated"] != true || s["userId"] != "u1" {
		t.Errorf("session body: %+v", s)
	}
}
