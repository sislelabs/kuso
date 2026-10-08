package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	httpsrv "kuso/server/internal/http"
	"kuso/server/internal/http/handlers"
)

func newTestServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d := openHandlerTestDB(t)

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
INSERT INTO "Role" (id, name, description, "createdAt", "updatedAt") VALUES ('r1', 'admin', '', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", "roleId", provider, "instanceRole", "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', $1, false, true, 'r1', 'local', 'admin', $2, $3)`, hash, now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	// Instance admin comes from the direct instanceRole above. The
	// role's settings:admin row is the legacy shape and is stripped as
	// reserved, so the role contributes only app:read.
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "Permission" (id, resource, action, "createdAt", "updatedAt")
VALUES ('p1', 'app', 'read', $1, $2), ('p2', 'settings', 'admin', $1, $2)`, now, now); err != nil {
		t.Fatalf("seed perm: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "_PermissionToRole" ("A", "B") VALUES ('p1', 'r1'), ('p2', 'r1')`); err != nil {
		t.Fatalf("seed pivot: %v", err)
	}
}

func TestLogin_Success(t *testing.T) {
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
	if !auth.Has(claims.Permissions, "app:read") || !auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		t.Errorf("permissions: %+v", claims.Permissions)
	}
}

// TestLogin_ByEmail verifies the login identifier accepts the account's
// email, not just its username. Users reach for their email by default
// (the invite-redeem flow sets username + email independently), so a
// username-only lookup would lock them out. The seeded admin's email is
// "a@b"; logging in with it must succeed and issue the same claims.
func TestLogin_ByEmail(t *testing.T) {
	r, d, iss := newTestServer(t)
	seedAdmin(t, d, "hunter2")

	body := strings.NewReader(`{"username":"a@b","password":"hunter2"}`)
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
	claims, err := iss.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "u1" {
		t.Errorf("expected u1 for email login, got %+v", claims)
	}
}

// TestLogin_ByEmailWrongPassword confirms the email-fallback path still
// gates on the password: a real email with a bad password is 401, not a
// bypass.
func TestLogin_ByEmailWrongPassword(t *testing.T) {
	r, d, _ := newTestServer(t)
	seedAdmin(t, d, "hunter2")

	body := strings.NewReader(`{"username":"a@b","password":"nope"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", body)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

func TestLogin_BadPassword(t *testing.T) {
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
	r, _, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader("{not-json"))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestSession_AfterLogin(t *testing.T) {
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

// Logging out a never-expire session wrote the revocation row with a
// now+24h bound, so the daily prune dropped it and the stolen cookie
// verified again the next day.
func TestLogout_NeverExpireTokenStaysRevoked(t *testing.T) {
	d := openHandlerTestDB(t)
	ctx := context.Background()
	if _, err := d.ExecContext(ctx, `TRUNCATE TABLE "RevokedToken"`); err != nil {
		t.Fatalf("truncate RevokedToken: %v", err)
	}
	iss, err := auth.NewIssuer("test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := iss.SignWithExpiry(auth.Claims{UserID: "u1"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	h := &handlers.AuthHandler{DB: d, Issuer: iss, Logger: slog.Default()}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	h.Logout(httptest.NewRecorder(), req)

	var exp time.Time
	if err := d.QueryRowContext(ctx, `SELECT "expiresAt" FROM "RevokedToken" WHERE "userId" = $1`, "u1").Scan(&exp); err != nil {
		t.Fatalf("revocation row: %v", err)
	}
	if exp.Before(time.Now().Add(365 * 24 * time.Hour)) {
		t.Fatalf("revocation for a never-expire token expires at %s; the prune will drop it", exp)
	}
}
