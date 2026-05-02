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

func newAdminServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "kuso.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	iss, _ := auth.NewIssuer("test-secret", time.Hour)
	r := httpsrv.NewRouter(httpsrv.Deps{DB: d, Issuer: iss, Logger: slog.Default()})
	return r, d, iss
}

func seedAdminUser(t *testing.T, d *db.DB) string {
	t.Helper()
	hash, _ := auth.HashPassword("hunter2", 4)
	now := time.Now().UTC()
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES ('r1', 'admin', ?, ?)`, now, now); err != nil {
		t.Fatalf("seed role: %v", err)
	}
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", "roleId", provider, "createdAt", "updatedAt")
VALUES ('u1', 'admin', 'a@b', ?, 0, 1, 'r1', 'local', ?, ?)`, hash, now, now); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return "u1"
}

func loginAndGetToken(t *testing.T, r http.Handler) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"hunter2"}`))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login: %d body=%s", rr.Code, rr.Body.String())
	}
	var lr struct {
		AccessToken string `json:"access_token"`
	}
	_ = json.NewDecoder(rr.Body).Decode(&lr)
	return lr.AccessToken
}

func TestAdmin_ListUsers(t *testing.T) {
	t.Parallel()
	r, d, _ := newAdminServer(t)
	seedAdminUser(t, d)
	tok := loginAndGetToken(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/users", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d, body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&out)
	if len(out) != 1 || out[0]["username"] != "admin" {
		t.Errorf("body: %+v", out)
	}
}

func TestAdmin_Profile(t *testing.T) {
	t.Parallel()
	r, d, _ := newAdminServer(t)
	seedAdminUser(t, d)
	tok := loginAndGetToken(t, r)

	req := httptest.NewRequest(http.MethodGet, "/api/users/profile", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	var p map[string]any
	_ = json.NewDecoder(rr.Body).Decode(&p)
	if p["username"] != "admin" || p["role"] != "admin" {
		t.Errorf("profile: %+v", p)
	}
}

func TestAdmin_Tokens_RoundTrip(t *testing.T) {
	t.Parallel()
	r, d, _ := newAdminServer(t)
	seedAdminUser(t, d)
	tok := loginAndGetToken(t, r)

	// Create
	body := strings.NewReader(`{"name":"ci","expiresAt":"` + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339) + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/tokens/my", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("create: %d body=%s", rr.Code, rr.Body.String())
	}
	var created struct {
		Token string `json:"token"`
	}
	_ = json.NewDecoder(rr.Body).Decode(&created)
	if created.Token == "" {
		t.Fatal("empty token")
	}

	// List
	listReq := httptest.NewRequest(http.MethodGet, "/api/tokens/my", nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list: %d", listRR.Code)
	}
	var rows []map[string]any
	_ = json.NewDecoder(listRR.Body).Decode(&rows)
	if len(rows) != 1 || rows[0]["name"] != "ci" {
		t.Errorf("list: %+v", rows)
	}

	id, _ := rows[0]["id"].(string)
	if id == "" {
		t.Fatal("token id missing")
	}

	// Delete
	delReq := httptest.NewRequest(http.MethodDelete, "/api/tokens/my/"+id, nil)
	delReq.Header.Set("Authorization", "Bearer "+tok)
	delRR := httptest.NewRecorder()
	r.ServeHTTP(delRR, delReq)
	if delRR.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", delRR.Code)
	}

	// List again — empty.
	emptyReq := httptest.NewRequest(http.MethodGet, "/api/tokens/my", nil)
	emptyReq.Header.Set("Authorization", "Bearer "+tok)
	emptyRR := httptest.NewRecorder()
	r.ServeHTTP(emptyRR, emptyReq)
	var empty []map[string]any
	_ = json.NewDecoder(emptyRR.Body).Decode(&empty)
	if len(empty) != 0 {
		t.Errorf("expected empty list after delete, got %+v", empty)
	}
}

func TestAdmin_Tokens_DeleteOtherUserFails(t *testing.T) {
	t.Parallel()
	r, d, _ := newAdminServer(t)
	seedAdminUser(t, d)
	// Insert a token belonging to a different user.
	if _, err := d.ExecContext(context.Background(), `
INSERT INTO "User" (id, username, email, password, "twoFaEnabled", "isActive", provider, "createdAt", "updatedAt")
VALUES ('u2', 'other', 'o@o', 'h', 0, 1, 'local', ?, ?)`, time.Now().UTC(), time.Now().UTC()); err != nil {
		t.Fatalf("seed other user: %v", err)
	}
	if err := d.CreateToken(context.Background(), &db.Token{
		ID: "t-other", UserID: "u2", ExpiresAt: time.Now().Add(time.Hour), IsActive: true,
	}); err != nil {
		t.Fatalf("seed token: %v", err)
	}
	tok := loginAndGetToken(t, r)

	delReq := httptest.NewRequest(http.MethodDelete, "/api/tokens/my/t-other", nil)
	delReq.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, delReq)
	if rr.Code != http.StatusNotFound {
		t.Errorf("expected 404 (cross-user delete), got %d", rr.Code)
	}
}
