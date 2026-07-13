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
)

// invites are mounted by the minimal Deps router (DB + Issuer), so they
// are testable end-to-end. We exercise the auth gate at every route:
// public lookup and admin-only Create/List/Revoke.

func newInviteServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d := openHandlerTestDB(t)
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
	r, _, _ := newInviteServer(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/db/stats", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401", rr.Code)
	}
}

func TestDBStats_Admin_ReturnsCounters(t *testing.T) {
	r, d, iss := newInviteServer(t)
	// Drive a write through the wrapper so the counter ticks. Schema
	// migrations call d.DB.Exec (no context) and intentionally bypass
	// the wrapper — they're rare and noisy.
	if _, err := d.ExecContext(context.Background(),
		`INSERT INTO "Role" (id, name, "createdAt", "updatedAt") VALUES ('r1','t',NOW(),NOW())`); err != nil {
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
		WriteErrors uint64 `json:"writeErrors"`
		PoolOpen    int    `json:"poolOpen"`
		PoolInUse   int    `json:"poolInUse"`
		PoolIdle    int    `json:"poolIdle"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// We don't assert exact pool values (depends on the test runner's
	// concurrent state) — just that the endpoint round-trips cleanly
	// and the new field shape is reachable.
	if snap.PoolOpen < 0 {
		t.Errorf("negative pool counter: %+v", snap)
	}
}

// --- local redemption (public signup path) ------------------------------

// End-to-end local redemption: the invite's configured group AND
// instanceRole must land on the created user atomically with the seat
// claim (S-review Finding 7 — the role used to be advertised but never
// applied).
func TestInvites_RedeemLocal_AppliesGroupAndRole(t *testing.T) {
	r, d, iss := newInviteServer(t)
	ctx := context.Background()
	if err := d.CreateGroup(ctx, "grp-eng", "engineering", ""); err != nil {
		t.Fatalf("group: %v", err)
	}
	role := "editor"
	gid := "grp-eng"
	if err := d.CreateInvite(ctx, db.CreateInviteInput{
		ID: "inv-local", Token: "tok-local", GroupID: &gid, InstanceRole: &role,
		CreatedBy: "admin", MaxUses: 1,
	}); err != nil {
		t.Fatalf("invite: %v", err)
	}

	body := strings.NewReader(`{"token":"tok-local","username":"newbie","email":"newbie@example.com","password":"hunter2hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites/redeem", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("redeem: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil || resp.AccessToken == "" {
		t.Fatalf("no access_token: err=%v body=%s", err, rr.Body.String())
	}
	claims, err := iss.Verify(resp.AccessToken)
	if err != nil {
		t.Fatalf("verify jwt: %v", err)
	}

	user, err := d.FindUserByUsername(ctx, "newbie")
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	if claims.UserID != user.ID {
		t.Errorf("jwt user mismatch: %s vs %s", claims.UserID, user.ID)
	}
	ten, err := d.ListUserTenancy(ctx, user.ID)
	if err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	if ten.InstanceRole != db.InstanceRoleEditor {
		t.Errorf("instance role: %q (want editor)", ten.InstanceRole)
	}
	groups, err := d.UserGroupNames(ctx, user.ID)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	if len(groups) != 1 || groups[0] != "engineering" {
		t.Errorf("groups: %v (want [engineering])", groups)
	}
	inv, err := d.FindInviteByToken(ctx, "tok-local")
	if err != nil {
		t.Fatalf("re-read invite: %v", err)
	}
	if inv.UsedCount != 1 {
		t.Errorf("usedCount: %d (want 1)", inv.UsedCount)
	}
}

// A taken username must not burn a seat — the redemption transaction
// rolls back whole.
func TestInvites_RedeemLocal_TakenUsernameKeepsSeat(t *testing.T) {
	r, d, _ := newInviteServer(t)
	ctx := context.Background()
	if err := d.CreateUser(ctx, db.CreateUserInput{
		ID: "u-first", Username: "dibs", Email: "dibs@example.com",
		PasswordHash: "hash", IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.CreateInvite(ctx, db.CreateInviteInput{
		ID: "inv-seat", Token: "tok-seat", CreatedBy: "admin", MaxUses: 1,
	}); err != nil {
		t.Fatalf("invite: %v", err)
	}

	body := strings.NewReader(`{"token":"tok-seat","username":"dibs","email":"other@example.com","password":"hunter2hunter2"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/invites/redeem", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status=%d want 409 body=%s", rr.Code, rr.Body.String())
	}
	inv, err := d.FindInviteByToken(ctx, "tok-seat")
	if err != nil {
		t.Fatalf("re-read invite: %v", err)
	}
	if inv.UsedCount != 0 {
		t.Errorf("seat burned: usedCount=%d (want 0)", inv.UsedCount)
	}
}
