package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/oauth2"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/http/handlers"
)

// newGithubMock spins up an httptest.Server that satisfies the slim
// subset of GitHub the OAuth callback exercises:
//
//   - POST /login/oauth/access_token  → opaque access_token + token_type
//   - GET  /user                       → {id, login, name, email}
//   - GET  /user/emails                → list (only hit if /user.email is empty)
//
// Tests configure GithubOAuth.Cfg.Endpoint.TokenURL at the mock and
// GithubOAuth.APIBase at the mock so both halves of the exchange
// reach the mock instead of api.github.com.
type githubMock struct {
	*httptest.Server
	wantCode    string
	accessToken string
	user        map[string]any
	emails      []map[string]any
	exchanges   int
	userHits    int
	emailHits   int
}

func newGithubMock(t *testing.T) *githubMock {
	t.Helper()
	gm := &githubMock{
		wantCode:    "code-deadbeef",
		accessToken: "gho_testtoken",
		user: map[string]any{
			"id":         int64(424242),
			"login":      "octocat",
			"name":       "Mona Octocat",
			"email":      "octocat@github.example",
			"avatar_url": "https://example.invalid/octocat.png",
		},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		gm.exchanges++
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		code := r.FormValue("code")
		if code != gm.wantCode {
			http.Error(w, "bad code", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": gm.accessToken,
			"token_type":   "bearer",
			"scope":        "read:user,user:email",
		})
	})
	mux.HandleFunc("/user", func(w http.ResponseWriter, r *http.Request) {
		gm.userHits++
		if r.Header.Get("Authorization") != "Bearer "+gm.accessToken {
			http.Error(w, "no auth", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gm.user)
	})
	mux.HandleFunc("/user/emails", func(w http.ResponseWriter, r *http.Request) {
		gm.emailHits++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(gm.emails)
	})
	gm.Server = httptest.NewServer(mux)
	t.Cleanup(gm.Close)
	return gm
}

// newOAuthHarness wires up the bits the OAuth handler needs against a
// fresh sqlite DB and an httptest GitHub mock.
func newOAuthHarness(t *testing.T) (*chi.Mux, *handlers.OAuthHandler, *githubMock, *db.DB) {
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
	gm := newGithubMock(t)
	gh := &auth.GithubOAuth{
		Cfg: &oauth2.Config{
			ClientID:     "test-client-id",
			ClientSecret: "test-client-secret",
			RedirectURL:  "https://kuso.example/api/auth/github/callback",
			Scopes:       []string{"read:user", "user:email"},
			Endpoint: oauth2.Endpoint{
				AuthURL:  gm.URL + "/login/oauth/authorize",
				TokenURL: gm.URL + "/login/oauth/access_token",
			},
		},
		APIBase: gm.URL,
	}
	h := &handlers.OAuthHandler{
		DB:     d,
		Issuer: iss,
		Github: gh,
		Logger: slog.Default(),
	}
	r := chi.NewRouter()
	h.MountPublic(r)
	return r, h, gm, d
}

// drive runs the full /api/auth/github → GitHub mock → /api/auth/github/callback
// roundtrip end-to-end. Returns the final response (the redirect to "/").
func drive(t *testing.T, r http.Handler, gm *githubMock) *httptest.ResponseRecorder {
	t.Helper()
	// Step 1 — start. Captures the state cookie + redirect to the mock.
	startReq := httptest.NewRequest(http.MethodGet, "/api/auth/github", nil)
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, startReq)
	if startRR.Code != http.StatusFound {
		t.Fatalf("start status: %d body=%q", startRR.Code, startRR.Body.String())
	}
	loc, err := url.Parse(startRR.Header().Get("Location"))
	if err != nil {
		t.Fatalf("parse Location: %v", err)
	}
	state := loc.Query().Get("state")
	if state == "" {
		t.Fatalf("no state in start redirect")
	}
	stateCookie := startRR.Result().Cookies()
	if len(stateCookie) == 0 {
		t.Fatalf("no Set-Cookie on start")
	}

	// Step 2 — callback. Browser sends the state cookie back + ?code=&state=.
	cbReq := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/auth/github/callback?code=%s&state=%s",
			url.QueryEscape(gm.wantCode), url.QueryEscape(state)),
		nil)
	for _, c := range stateCookie {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	return cbRR
}

func TestOAuth_FullCallbackHappyPath(t *testing.T) {
	t.Parallel()
	r, _, gm, _ := newOAuthHarness(t)

	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback status: %d body=%q", rr.Code, rr.Body.String())
	}
	// The redirect target is now "/#token=<jwt>" — the URL fragment
	// hands the JWT to the SPA without exposing it to the server logs
	// or to any non-same-origin redirect tracker. The fragment is
	// stripped by api-client.ts on first paint and the bearer is
	// stored in localStorage. See setJWTCookie + redirectWithJWT
	// for the change. Test asserts the prefix instead of equality
	// so the JWT contents aren't pinned.
	got := rr.Header().Get("Location")
	if !strings.HasPrefix(got, "/#token=") {
		t.Errorf("redirect target: %q (want prefix \"/#token=\")", got)
	}
	if gm.exchanges != 1 {
		t.Errorf("exchanges: %d (want 1)", gm.exchanges)
	}
	if gm.userHits != 1 {
		t.Errorf("user hits: %d (want 1)", gm.userHits)
	}
}

func TestOAuth_SetsJWTCookie(t *testing.T) {
	t.Parallel()
	r, _, gm, _ := newOAuthHarness(t)
	rr := drive(t, r, gm)

	var jwt *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "kuso.JWT_TOKEN" {
			jwt = c
			break
		}
	}
	if jwt == nil {
		t.Fatalf("kuso.JWT_TOKEN not set in callback response: %+v", rr.Result().Cookies())
	}
	// The cookie is now HttpOnly. The SPA receives the JWT via the
	// URL fragment (#token=…) and the cookie is server-only — used
	// by the WebSocket log handler where the browser can't set
	// Authorization. Asserting HttpOnly defends against accidental
	// regression to the legacy JS-readable shape that made every
	// XSS in the bundle equivalent to session theft.
	if !jwt.HttpOnly {
		t.Error("kuso.JWT_TOKEN must be HttpOnly to prevent XSS-driven token theft")
	}
	if !jwt.Secure {
		t.Error("kuso.JWT_TOKEN must be Secure")
	}
	if jwt.SameSite != http.SameSiteLaxMode {
		t.Errorf("SameSite: got %v (want Lax)", jwt.SameSite)
	}
	if jwt.Path != "/" {
		t.Errorf("Path: %q (want /)", jwt.Path)
	}
	if jwt.MaxAge <= 0 {
		t.Errorf("MaxAge: %d (want > 0)", jwt.MaxAge)
	}
	if jwt.Value == "" {
		t.Error("empty JWT value")
	}
}

func TestOAuth_UpsertsUserAndIssuesValidJWT(t *testing.T) {
	t.Parallel()
	r, h, gm, d := newOAuthHarness(t)
	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}

	// User should exist now under the GitHub login.
	user, err := d.FindUserByUsername(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
	if user.Email != "octocat@github.example" {
		t.Errorf("email: %q", user.Email)
	}

	// JWT round-trips and carries strategy=oauth2.
	var jwt string
	for _, c := range rr.Result().Cookies() {
		if c.Name == "kuso.JWT_TOKEN" {
			jwt = c.Value
			break
		}
	}
	if jwt == "" {
		t.Fatal("no JWT cookie")
	}
	claims, err := h.Issuer.Verify(jwt)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != user.ID || claims.Username != "octocat" {
		t.Errorf("claims: %+v", claims)
	}
	if claims.Strategy != "oauth2" {
		t.Errorf("strategy: %q (want oauth2)", claims.Strategy)
	}
}

func TestOAuth_RejectsStateMismatch(t *testing.T) {
	t.Parallel()
	r, _, gm, _ := newOAuthHarness(t)

	// Hit start to plant the state cookie, then call callback with a
	// different state value to simulate a CSRF attempt.
	startReq := httptest.NewRequest(http.MethodGet, "/api/auth/github", nil)
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, startReq)

	cbReq := httptest.NewRequest(http.MethodGet,
		"/api/auth/github/callback?code="+gm.wantCode+"&state=not-our-state",
		nil)
	for _, c := range startRR.Result().Cookies() {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	if cbRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", cbRR.Code, cbRR.Body.String())
	}
}

func TestOAuth_RejectsMissingStateCookie(t *testing.T) {
	t.Parallel()
	r, _, gm, _ := newOAuthHarness(t)
	cbReq := httptest.NewRequest(http.MethodGet,
		"/api/auth/github/callback?code="+gm.wantCode+"&state=anything",
		nil)
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	if cbRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", cbRR.Code)
	}
	if gm.exchanges != 0 {
		t.Errorf("must not have exchanged code without valid state cookie; exchanges=%d", gm.exchanges)
	}
}

func TestOAuth_RejectsMissingCode(t *testing.T) {
	t.Parallel()
	r, _, _, _ := newOAuthHarness(t)
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, httptest.NewRequest(http.MethodGet, "/api/auth/github", nil))
	loc, _ := url.Parse(startRR.Header().Get("Location"))
	state := loc.Query().Get("state")

	cbReq := httptest.NewRequest(http.MethodGet,
		"/api/auth/github/callback?state="+url.QueryEscape(state), nil)
	for _, c := range startRR.Result().Cookies() {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	if cbRR.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on missing code, got %d", cbRR.Code)
	}
}

func TestOAuth_FallsBackToUserEmailsWhenPrimaryHidden(t *testing.T) {
	t.Parallel()
	r, _, gm, d := newOAuthHarness(t)
	// GitHub returns hidden-email users with email="" on /user. The
	// handler should follow up with /user/emails and pick the first
	// primary+verified.
	gm.user["email"] = ""
	gm.emails = []map[string]any{
		{"email": "noise@example.com", "primary": false, "verified": true},
		{"email": "real@example.com", "primary": true, "verified": true},
	}

	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	if gm.emailHits != 1 {
		t.Errorf("emails endpoint hit %d times (want 1 fallback)", gm.emailHits)
	}
	user, err := d.FindUserByUsername(context.Background(), "octocat")
	if err != nil {
		t.Fatalf("FindUserByUsername: %v", err)
	}
	if user.Email != "real@example.com" {
		t.Errorf("email fallback failed: %q", user.Email)
	}
}

// Sanity: make sure the start redirect lands on the configured AuthURL,
// not a hardcoded github.com URL — protects against accidental loss of
// the test override and detects the real-vs-mock plumbing breaking.
func TestOAuth_StartHonorsConfiguredEndpoint(t *testing.T) {
	t.Parallel()
	r, _, gm, _ := newOAuthHarness(t)
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, httptest.NewRequest(http.MethodGet, "/api/auth/github", nil))
	loc := startRR.Header().Get("Location")
	if !strings.HasPrefix(loc, gm.URL+"/login/oauth/authorize?") {
		t.Errorf("start redirected to %q; want it to begin with %s/login/oauth/authorize", loc, gm.URL)
	}
}
