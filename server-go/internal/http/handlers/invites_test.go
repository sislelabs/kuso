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

// invites are mounted by the minimal Deps router (DB + Issuer), so they
// are testable end-to-end. We exercise the auth gate at every route:
// public lookup and admin-only Create/List/Revoke.

func newInviteServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
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

// mintToken signs a JWT directly. Bypasses /api/auth/login (and the
// per-IP rate limiter that fronts it) so parallel tests can each carry
// their own claims without serializing on the limiter.
func mintToken(t *testing.T, iss *auth.Issuer, userID string, perms ...auth.Permission) string {
	t.Helper()
	strs := make([]string, 0, len(perms))
	for _, p := range perms {
		strs = append(strs, string(p))
	}
	tok, err := iss.Sign(auth.Claims{
		UserID:      userID,
		Username:    userID,
		Strategy:    "local",
		Permissions: strs,
	})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	return tok
}

// --- middleware-level checks ------------------------------------------

func TestInvites_NoAuth_401(t *testing.T) {
	t.Parallel()
	r, _, _ := newInviteServer(t)
	for _, route := range []struct {
		method, path string
	}{
		{http.MethodGet, "/api/invites"},
		{http.MethodPost, "/api/invites"},
		{http.MethodDelete, "/api/invites/whatever"},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status=%d want 401 body=%q", route.method, route.path, rr.Code, rr.Body.String())
		}
	}
}

func TestInvites_BadToken_401(t *testing.T) {
	t.Parallel()
	r, _, _ := newInviteServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/invites", nil)
	req.Header.Set("Authorization", "Bearer not-a-real-jwt")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

// --- permission checks ------------------------------------------------

func TestInvites_NonAdmin_403(t *testing.T) {
	t.Parallel()
	r, _, iss := newInviteServer(t)
	// No PermUserWrite → both admin routes must 403.
	tok := mintToken(t, iss, "bob", auth.PermProjectRead)

	body := strings.NewReader(`{"maxUses":1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("Create as non-admin: status=%d want 403 body=%q", rr.Code, rr.Body.String())
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/invites", nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusForbidden {
		t.Errorf("List as non-admin: status=%d want 403", listRR.Code)
	}
}

// --- happy path -------------------------------------------------------

func TestInvites_Admin_CreateListRevoke(t *testing.T) {
	t.Parallel()
	r, _, iss := newInviteServer(t)
	tok := mintToken(t, iss, "admin", auth.PermUserWrite)

	createBody := strings.NewReader(`{"maxUses":1,"note":"for ci"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/api/invites", createBody)
	createReq.Header.Set("Authorization", "Bearer "+tok)
	createReq.Header.Set("Content-Type", "application/json")
	createRR := httptest.NewRecorder()
	r.ServeHTTP(createRR, createReq)
	if createRR.Code != http.StatusCreated && createRR.Code != http.StatusOK {
		t.Fatalf("Create: status=%d body=%s", createRR.Code, createRR.Body.String())
	}
	var created struct {
		Invite struct {
			ID    string `json:"id"`
			Token string `json:"token"`
		} `json:"invite"`
		URL string `json:"url"`
	}
	_ = json.NewDecoder(createRR.Body).Decode(&created)
	if created.Invite.ID == "" || created.Invite.Token == "" {
		t.Fatalf("Create body shape: %+v", created)
	}
	if !strings.Contains(created.URL, "/invite/"+created.Invite.Token) {
		t.Errorf("invite URL missing token: %s", created.URL)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/invites", nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("List: status=%d body=%s", listRR.Code, listRR.Body.String())
	}
	var rows []map[string]any
	_ = json.NewDecoder(listRR.Body).Decode(&rows)
	if len(rows) != 1 {
		t.Errorf("expected 1 invite, got %d", len(rows))
	}

	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/invites/"+created.Invite.ID, nil)
	revokeReq.Header.Set("Authorization", "Bearer "+tok)
	revokeRR := httptest.NewRecorder()
	r.ServeHTTP(revokeRR, revokeReq)
	if revokeRR.Code != http.StatusNoContent {
		t.Errorf("Revoke: status=%d body=%s", revokeRR.Code, revokeRR.Body.String())
	}
}

// --- input validation gates -------------------------------------------

func TestInvites_MultiUseRequiresExpiry(t *testing.T) {
	t.Parallel()
	r, _, iss := newInviteServer(t)
	tok := mintToken(t, iss, "admin", auth.PermUserWrite)

	body := strings.NewReader(`{"maxUses":5}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("multi-use without expiry: status=%d want 400 body=%q", rr.Code, rr.Body.String())
	}
}

func TestInvites_NegativeMaxUses_400(t *testing.T) {
	t.Parallel()
	r, _, iss := newInviteServer(t)
	tok := mintToken(t, iss, "admin", auth.PermUserWrite)

	body := strings.NewReader(`{"maxUses":-1}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400", rr.Code)
	}
}

// --- public lookup is genuinely unauthenticated -----------------------

func TestInvites_PublicLookup_NoAuth(t *testing.T) {
	t.Parallel()
	r, _, _ := newInviteServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/invites/lookup/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	// Unknown token → 404, NOT 401. This is the contract: invitees
	// have no JWT, the auth middleware must not gate the lookup.
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404 (and definitely not 401) body=%q", rr.Code, rr.Body.String())
	}
}

// --- /api/admin/db/stats ----------------------------------------------

func TestDBStats_RequiresAdmin(t *testing.T) {
	t.Parallel()
	r, _, iss := newInviteServer(t)
	tok := mintToken(t, iss, "bob", auth.PermProjectRead)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/db/stats", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("non-admin: status=%d want 403", rr.Code)
	}
}

func TestDBStats_NoAuth_401(t *testing.T) {
	t.Parallel()
	r, _, _ := newInviteServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/db/stats", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestDBStats_Admin_ReturnsCounters(t *testing.T) {
	t.Parallel()
	r, d, iss := newInviteServer(t)
	// Drive a write through the wrapper so the counter ticks. Schema
	// migrations call d.DB.Exec (no context) and intentionally bypass
	// the wrapper — they're rare and noisy.
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES ('r1','t',datetime('now'),datetime('now'))`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	tok := mintToken(t, iss, "admin", auth.PermSettingsAdmin)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/db/stats", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var snap struct {
		WriteCount     uint64 `json:"writeCount"`
		BusyCount      uint64 `json:"busyCount"`
		WriteWaitMs    int64  `json:"writeWaitMs"`
		BusyWaitMs     int64  `json:"busyWaitMs"`
		AvgWriteWaitMs int64  `json:"avgWriteWaitMs"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.WriteCount == 0 {
		t.Errorf("expected writeCount>0 after seed, got %+v", snap)
	}
	if snap.BusyCount != 0 {
		t.Errorf("idle DB busyCount=%d, want 0", snap.BusyCount)
	}
}
