package http

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"kuso/server/internal/auth"
	httphandlers "kuso/server/internal/http/handlers"
	"kuso/server/internal/version"
)

// healthz must remain unauthenticated and stable in shape — Phase 0
// shipped {status, version}; the Vue client + uptime probes depend on
// it.
func TestHealthz_Unauthenticated(t *testing.T) {
	t.Parallel()
	iss, err := auth.NewIssuer("test-secret-irrelevant-for-this-route", 0)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, want %d", rr.Code, http.StatusOK)
	}
	var body map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" || body["version"] != version.Version() {
		t.Errorf("body: %+v", body)
	}
}

func TestSession_RequiresBearer(t *testing.T) {
	t.Parallel()
	iss, _ := auth.NewIssuer("test-secret", 0)
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})

	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rr.Code)
	}
}

// TestMaxBodyBytes_ImportRouteExempt — the global 1 MiB body cap must
// not apply to /api/projects/import, whose documented cap is 16 MiB
// (httphandlers.MaxImportRequestBytes). Everything else keeps 1 MiB.
func TestMaxBodyBytes_ImportRouteExempt(t *testing.T) {
	t.Parallel()
	mw := maxBodyBytes(1 << 20)
	drain := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	h := mw(drain)

	body := func(n int) *bytes.Reader { return bytes.NewReader(make([]byte, n)) }

	// 2 MiB on a normal route → capped.
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects", body(2<<20)))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("normal route 2 MiB: status = %d, want 413", rr.Code)
	}

	// 2 MiB on the import route → allowed.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects/import", body(2<<20)))
	if rr.Code != http.StatusOK {
		t.Errorf("import route 2 MiB: status = %d, want 200", rr.Code)
	}

	// Over 16 MiB on the import route → still capped.
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/projects/import", body(int(httphandlers.MaxImportRequestBytes)+1)))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("import route 16 MiB+1: status = %d, want 413", rr.Code)
	}
}

// TestHasBearerAuth_MatchesMiddlewareAuth locks the CSRF skip predicate
// to the credential the auth middleware actually uses.
//
// Regression guard. hasBearerAuth used to accept a case-INSENSITIVE
// prefix (EqualFold) plus a bare length check, while auth.bearerToken
// requires a case-sensitive "Bearer " and a non-empty token. Headers in
// that gap ("bearer x", "BEARER x", "Bearer \t") skipped the CSRF origin
// check but did NOT authenticate via the header — so the request rode
// the victim's session cookie instead. That is a CSRF bypass.
//
// The invariant: hasBearerAuth(r) may be true ONLY when the header alone
// authenticates. If a future edit re-introduces a mismatch, this fails.
func TestHasBearerAuth_MatchesMiddlewareAuth(t *testing.T) {
	t.Parallel()

	iss, err := auth.NewIssuer("test-secret", 0)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	// A handler that only runs when the bearer middleware accepted the
	// request; the middleware 401s otherwise.
	var authed bool
	chain := iss.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		authed = true
	}))

	tok, err := iss.Sign(auth.Claims{UserID: "u1", Username: "u", Role: "admin"})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	headers := []string{
		"Bearer " + tok, // canonical — authenticates, may skip CSRF
		"bearer " + tok, // lowercase scheme
		"BEARER " + tok, // uppercase scheme
		"BeArEr " + tok, // mixed case
		"Bearer ",       // empty token
		"Bearer  ",      // whitespace-only token
		"Bearer \t",     // tab-only token
		"Basic " + tok,  // wrong scheme
		"",              // absent
	}

	for _, h := range headers {
		req := httptest.NewRequest(http.MethodPost, "/api/anything", nil)
		if h != "" {
			req.Header.Set("Authorization", h)
		}

		skipsCSRF := hasBearerAuth(req)

		authed = false
		iss.ResolvePermissions(req.Context(), nil) // no-op; keeps ctx shape honest
		chain.ServeHTTP(httptest.NewRecorder(), req)
		headerAuthenticates := authed

		if skipsCSRF && !headerAuthenticates {
			t.Errorf("Authorization=%q: skips CSRF check but does NOT authenticate via the header "+
				"— the request would fall back to the session cookie (CSRF bypass)", h)
		}
	}
}
