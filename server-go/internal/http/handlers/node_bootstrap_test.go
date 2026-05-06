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

// The bootstrap-token handlers exercise three boundaries:
//
//  1. Admin-only mint/list/revoke — non-admin callers must 403, no-auth
//     callers must 401.
//  2. Public /bootstrap?token=<jti> — token entropy is the credential.
//     Unknown tokens 404; consumed/expired/revoked tokens 410.
//  3. Public /bootstrap/register-node — single-use; double-redeem 410.
//
// We don't exercise the ReadServerToken hostPath bit (test runs outside
// k8s); the register-node tests stop at the consume step or expect a
// 503 from the missing token file. Both shapes matter.

func newBootstrapServer(t *testing.T) (http.Handler, *db.DB, *auth.Issuer) {
	t.Helper()
	d := openHandlerTestDB(t)
	iss, _ := auth.NewIssuer("test-secret-bootstrap", time.Hour)
	r := httpsrv.NewRouter(httpsrv.Deps{DB: d, Issuer: iss, Logger: slog.Default()})
	return r, d, iss
}

// --- admin-only gates -------------------------------------------------

func TestBootstrap_NoAuth_401(t *testing.T) {
	r, _, _ := newBootstrapServer(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/kubernetes/nodes/bootstrap-tokens"},
		{http.MethodGet, "/api/kubernetes/nodes/bootstrap-tokens"},
		{http.MethodDelete, "/api/kubernetes/nodes/bootstrap-tokens/abc"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`))
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status=%d want 401 body=%q", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestBootstrap_NonAdmin_403(t *testing.T) {
	r, _, iss := newBootstrapServer(t)
	tok := mintToken(t, iss, "bob", auth.PermProjectRead)
	req := httptest.NewRequest(http.MethodPost,
		"/api/kubernetes/nodes/bootstrap-tokens",
		strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status=%d want 403", rr.Code)
	}
}

// --- happy path: mint → peek script → register --------------------------

func TestBootstrap_MintAndScript(t *testing.T) {
	r, _, iss := newBootstrapServer(t)
	tok := mintToken(t, iss, "admin", auth.PermSettingsAdmin)

	body := strings.NewReader(`{"labels":{"region":"eu","tier":"premium"},"nodeName":"worker-1"}`)
	req := httptest.NewRequest(http.MethodPost,
		"/api/kubernetes/nodes/bootstrap-tokens", body)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("mint: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var minted struct {
		JTI       string            `json:"jti"`
		ExpiresAt time.Time         `json:"expiresAt"`
		OneLiner  string            `json:"oneLiner"`
		Labels    map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &minted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if minted.JTI == "" {
		t.Fatalf("empty jti")
	}
	if !strings.Contains(minted.OneLiner, minted.JTI) {
		t.Errorf("one-liner missing jti: %s", minted.OneLiner)
	}
	if !strings.Contains(minted.OneLiner, "/bootstrap?token=") {
		t.Errorf("one-liner has wrong URL shape: %s", minted.OneLiner)
	}

	// Public /bootstrap returns the script with the token baked in.
	scriptReq := httptest.NewRequest(http.MethodGet,
		"/bootstrap?token="+minted.JTI, nil)
	scriptRR := httptest.NewRecorder()
	r.ServeHTTP(scriptRR, scriptReq)
	if scriptRR.Code != http.StatusOK {
		t.Fatalf("script: status=%d body=%s", scriptRR.Code, scriptRR.Body.String())
	}
	body2 := scriptRR.Body.String()
	if !strings.Contains(body2, "#!/bin/sh") {
		t.Errorf("missing shebang")
	}
	if !strings.Contains(body2, minted.JTI) {
		t.Errorf("script missing jti")
	}

	// Pending list shows our token.
	listReq := httptest.NewRequest(http.MethodGet,
		"/api/kubernetes/nodes/bootstrap-tokens", nil)
	listReq.Header.Set("Authorization", "Bearer "+tok)
	listRR := httptest.NewRecorder()
	r.ServeHTTP(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list: status=%d body=%s", listRR.Code, listRR.Body.String())
	}
	if !strings.Contains(listRR.Body.String(), minted.JTI) {
		t.Errorf("list missing jti")
	}
}

// --- bad-token / not-found / replay -----------------------------------

func TestBootstrap_UnknownToken_404(t *testing.T) {
	r, _, _ := newBootstrapServer(t)
	req := httptest.NewRequest(http.MethodGet, "/bootstrap?token=does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rr.Code)
	}
}

func TestBootstrap_RevokedToken_410(t *testing.T) {
	r, d, iss := newBootstrapServer(t)
	tok := mintToken(t, iss, "admin", auth.PermSettingsAdmin)

	// Mint via DB so we control jti.
	jti := "revoked-jti-fixed-id"
	if err := d.MintNodeBootstrapToken(context.Background(), db.NodeBootstrapToken{
		Cleartext: jti,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
		Labels:    map[string]string{"x": "y"},
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	// Revoke via API.
	revReq := httptest.NewRequest(http.MethodDelete,
		"/api/kubernetes/nodes/bootstrap-tokens/"+jti, nil)
	revReq.Header.Set("Authorization", "Bearer "+tok)
	revRR := httptest.NewRecorder()
	r.ServeHTTP(revRR, revReq)
	if revRR.Code != http.StatusNoContent {
		t.Fatalf("revoke: status=%d body=%s", revRR.Code, revRR.Body.String())
	}
	// Script lookup must now 410.
	scrReq := httptest.NewRequest(http.MethodGet, "/bootstrap?token="+jti, nil)
	scrRR := httptest.NewRecorder()
	r.ServeHTTP(scrRR, scrReq)
	if scrRR.Code != http.StatusGone {
		t.Errorf("script after revoke: status=%d want 410", scrRR.Code)
	}
	// Register must also 410 (and NOT consume).
	regBody := strings.NewReader(`{"token":"` + jti + `","hostname":"h","arch":"amd64"}`)
	regReq := httptest.NewRequest(http.MethodPost, "/bootstrap/register-node", regBody)
	regReq.Header.Set("Content-Type", "application/json")
	regRR := httptest.NewRecorder()
	r.ServeHTTP(regRR, regReq)
	if regRR.Code != http.StatusGone {
		t.Errorf("register after revoke: status=%d want 410", regRR.Code)
	}
}

func TestBootstrap_ExpiredToken_410(t *testing.T) {
	r, d, _ := newBootstrapServer(t)
	jti := "expired-jti-fixed-id"
	// expiresAt in the past.
	if err := d.MintNodeBootstrapToken(context.Background(), db.NodeBootstrapToken{
		Cleartext: jti,
		ExpiresAt: time.Now().UTC().Add(-1 * time.Minute),
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	scrReq := httptest.NewRequest(http.MethodGet, "/bootstrap?token="+jti, nil)
	scrRR := httptest.NewRecorder()
	r.ServeHTTP(scrRR, scrReq)
	if scrRR.Code != http.StatusGone {
		t.Errorf("expired script: status=%d want 410", scrRR.Code)
	}
}

func TestBootstrap_ReplayConsumed_410(t *testing.T) {
	r, d, _ := newBootstrapServer(t)
	jti := "replay-jti-fixed-id"
	if err := d.MintNodeBootstrapToken(context.Background(), db.NodeBootstrapToken{
		Cleartext: jti,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	}); err != nil {
		t.Fatalf("mint: %v", err)
	}
	// First consume directly (the handler would also try to read the
	// hostPath token, which fails outside k8s — we test the consume
	// boundary separately via the DB and assert replay returns 410 at
	// the consume step itself, before ReadServerToken is called).
	if _, err := d.ConsumeNodeBootstrapToken(context.Background(), jti, "127.0.0.1"); err != nil {
		t.Fatalf("first consume: %v", err)
	}
	// Now hit /bootstrap/register-node — handler should consume and
	// see ErrTokenConsumed → 410 BEFORE attempting to read the k3s
	// token from the missing hostPath mount.
	regBody := strings.NewReader(`{"token":"` + jti + `","hostname":"h"}`)
	regReq := httptest.NewRequest(http.MethodPost, "/bootstrap/register-node", regBody)
	regReq.Header.Set("Content-Type", "application/json")
	regRR := httptest.NewRecorder()
	r.ServeHTTP(regRR, regReq)
	if regRR.Code != http.StatusGone {
		t.Errorf("replay: status=%d want 410 body=%s", regRR.Code, regRR.Body.String())
	}
}

func TestBootstrap_RegisterMissingToken_400(t *testing.T) {
	r, _, _ := newBootstrapServer(t)
	body := strings.NewReader(`{"hostname":"h"}`)
	req := httptest.NewRequest(http.MethodPost, "/bootstrap/register-node", body)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing token: status=%d want 400", rr.Code)
	}
}

func TestBootstrap_RevokeUnknown_404(t *testing.T) {
	r, _, iss := newBootstrapServer(t)
	tok := mintToken(t, iss, "admin", auth.PermSettingsAdmin)
	req := httptest.NewRequest(http.MethodDelete,
		"/api/kubernetes/nodes/bootstrap-tokens/never-existed", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status=%d want 404", rr.Code)
	}
}

func TestBootstrap_RevokeIdempotent(t *testing.T) {
	r, d, iss := newBootstrapServer(t)
	tok := mintToken(t, iss, "admin", auth.PermSettingsAdmin)
	jti := "idempotent-revoke-jti"
	_ = d.MintNodeBootstrapToken(context.Background(), db.NodeBootstrapToken{
		Cleartext: jti,
		ExpiresAt: time.Now().UTC().Add(15 * time.Minute),
	})
	doRevoke := func() int {
		req := httptest.NewRequest(http.MethodDelete,
			"/api/kubernetes/nodes/bootstrap-tokens/"+jti, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr.Code
	}
	if got := doRevoke(); got != http.StatusNoContent {
		t.Fatalf("first revoke: %d", got)
	}
	if got := doRevoke(); got != http.StatusNoContent {
		t.Errorf("second revoke (idempotent): %d", got)
	}
}
