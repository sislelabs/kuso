package handlers_test

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	d := openHandlerTestDB(t)

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
	return driveWithCookies(t, r, gm)
}

// driveWithCookies is drive with extra cookies attached to the
// callback request — used to exercise the invite-cookie flow. All
// requests carry X-Forwarded-Proto: https (TLS-terminating-proxy
// shape) so the Secure cookie attribute is exercised the way
// production sees it.
func driveWithCookies(t *testing.T, r http.Handler, gm *githubMock, extra ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	// Step 1 — start. Captures the state cookie + redirect to the mock.
	startReq := httptest.NewRequest(http.MethodGet, "/api/auth/github", nil)
	startReq.Header.Set("X-Forwarded-Proto", "https")
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
	cbReq.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range stateCookie {
		cbReq.AddCookie(c)
	}
	for _, c := range extra {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	return cbRR
}

func TestOAuth_FullCallbackHappyPath(t *testing.T) {
	r, _, gm, _ := newOAuthHarness(t)

	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback status: %d body=%q", rr.Code, rr.Body.String())
	}
	// The redirect target is "/?login=ok" — the JWT travels only in
	// the HttpOnly kuso.JWT_TOKEN cookie (see redirectWithJWT); the
	// legacy "/#token=<jwt>" fragment delivery is closed so the token
	// never sits in browser history / referers.
	if got := rr.Header().Get("Location"); got != "/?login=ok" {
		t.Errorf("redirect target: %q (want \"/?login=ok\")", got)
	}
	if gm.exchanges != 1 {
		t.Errorf("exchanges: %d (want 1)", gm.exchanges)
	}
	if gm.userHits != 1 {
		t.Errorf("user hits: %d (want 1)", gm.userHits)
	}
}

func TestOAuth_SetsJWTCookie(t *testing.T) {
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

// TestOAuth_ForwardsAppInstallRedirect covers the self-heal for a
// GitHub App whose Setup URL is (mis)pointed at the OAuth callback:
// GitHub sends the post-install hop here with setup_action/installation_id
// and NO state. Instead of "state mismatch", we forward to the install
// handler with the query intact.
func TestOAuth_ForwardsAppInstallRedirect(t *testing.T) {
	r, _, _, _ := newOAuthHarness(t)

	cbReq := httptest.NewRequest(http.MethodGet,
		"/api/auth/github/callback?code=dc7747c645e12450ffba&installation_id=139593223&setup_action=install",
		nil)
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)

	if cbRR.Code != http.StatusFound {
		t.Fatalf("expected 302 forward, got %d body=%q", cbRR.Code, cbRR.Body.String())
	}
	loc := cbRR.Header().Get("Location")
	wantPath := "/api/github/setup-callback"
	if !strings.HasPrefix(loc, wantPath+"?") {
		t.Fatalf("expected forward to %s with query, got %q", wantPath, loc)
	}
	// Query must survive so the install handler still sees installation_id.
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse location: %v", err)
	}
	if got := u.Query().Get("installation_id"); got != "139593223" {
		t.Errorf("installation_id should be preserved, got %q", got)
	}
	if got := u.Query().Get("setup_action"); got != "install" {
		t.Errorf("setup_action should be preserved, got %q", got)
	}
}

func TestOAuth_RejectsMissingStateCookie(t *testing.T) {
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
	r, _, gm, _ := newOAuthHarness(t)
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, httptest.NewRequest(http.MethodGet, "/api/auth/github", nil))
	loc := startRR.Header().Get("Location")
	if !strings.HasPrefix(loc, gm.URL+"/login/oauth/authorize?") {
		t.Errorf("start redirected to %q; want it to begin with %s/login/oauth/authorize", loc, gm.URL)
	}
}

// --- provider-identity resolution (S-review Finding 1) -----------------

// jwtCookie extracts the session cookie from a callback response, or
// nil when the flow didn't establish a session.
func jwtCookie(rr *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rr.Result().Cookies() {
		if c.Name == "kuso.JWT_TOKEN" && c.Value != "" {
			return c
		}
	}
	return nil
}

// A user already linked to this exact (provider, providerID) logs in
// as that account even when their kuso username no longer matches the
// GitHub login — the immutable ID wins, not the mutable username.
func TestOAuth_ResolvesByProviderID_NotUsername(t *testing.T) {
	r, h, gm, d := newOAuthHarness(t)
	ctx := context.Background()
	stub, _ := auth.HashPassword("irrelevant", auth.StubPasswordCost)
	if err := d.CreateOAuthUser(ctx, db.CreateOAuthUserInput{
		ID: "u-linked", Username: "renamed-locally", Email: "linked@example.com",
		PasswordHash: stub, Provider: "github", ProviderID: "424242",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	c := jwtCookie(rr)
	if c == nil {
		t.Fatal("no session cookie")
	}
	claims, err := h.Issuer.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.UserID != "u-linked" || claims.Username != "renamed-locally" {
		t.Errorf("resolved wrong account: %+v", claims)
	}
	// No second account under the GitHub login may have been created.
	if _, err := d.FindUserByUsername(ctx, "octocat"); err == nil {
		t.Error("a duplicate user was created under the provider username")
	}
}

// A GitHub login whose username collides with an existing local
// password account must NOT authenticate as (or link to) that account.
func TestOAuth_UsernameCollision_LocalAccount_Rejected(t *testing.T) {
	r, _, gm, d := newOAuthHarness(t)
	ctx := context.Background()
	hash, _ := auth.HashPassword("a-real-password", 0)
	if err := d.CreateUser(ctx, db.CreateUserInput{
		ID: "u-victim", Username: "octocat", Email: "victim@example.com",
		PasswordHash: hash, IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := drive(t, r, gm)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status: %d (want 409) body=%q", rr.Code, rr.Body.String())
	}
	if jwtCookie(rr) != nil {
		t.Error("session cookie issued despite account collision")
	}
	u, err := d.FindUserByID(ctx, "u-victim")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if u.ProviderID.Valid && u.ProviderID.String != "" {
		t.Errorf("victim account got silently linked: providerId=%q", u.ProviderID.String)
	}
}

// Email collisions are rejected the same way username collisions are.
func TestOAuth_EmailCollision_Rejected(t *testing.T) {
	r, _, gm, d := newOAuthHarness(t)
	hash, _ := auth.HashPassword("a-real-password", 0)
	if err := d.CreateUser(context.Background(), db.CreateUserInput{
		ID: "u-mail", Username: "not-octocat", Email: "octocat@github.example",
		PasswordHash: hash, IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := drive(t, r, gm)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status: %d (want 409) body=%q", rr.Code, rr.Body.String())
	}
	if jwtCookie(rr) != nil {
		t.Error("session cookie issued despite email collision")
	}
}

// Migration path: an account created before provider IDs were recorded
// (stub password, no providerId) whose GithubUserLink row maps this
// exact githubId to it gets auto-linked and logs in.
func TestOAuth_AutoLinksViaGithubUserLink(t *testing.T) {
	r, _, gm, d := newOAuthHarness(t)
	ctx := context.Background()
	stub, _ := auth.HashPassword("throwaway", auth.StubPasswordCost)
	if err := d.CreateUser(ctx, db.CreateUserInput{
		ID: "u-legacy", Username: "octocat", Email: "octocat@github.example",
		PasswordHash: stub, IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := d.UpsertGithubUserLink(ctx, db.GithubUserLink{
		UserID: "u-legacy", GithubLogin: "octocat", GithubID: 424242,
	}); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	rr := drive(t, r, gm)
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	u, err := d.FindUserByProvider(ctx, "github", "424242")
	if err != nil {
		t.Fatalf("account was not linked: %v", err)
	}
	if u.ID != "u-legacy" {
		t.Errorf("linked wrong account: %s", u.ID)
	}
}

// newOAuth2Harness wires the GENERIC oauth2 provider against a fresh
// DB and an httptest IdP that returns a fixed userinfo document. Used
// to prove the generic-provider collision behaviour independently of
// the GitHub path.
func newOAuth2Harness(t *testing.T, userinfo map[string]any) (*chi.Mux, *db.DB, string) {
	t.Helper()
	d := openHandlerTestDB(t)
	iss, err := auth.NewIssuer("test-secret", time.Hour)
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	const code = "oauth2-code"
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("code") != code {
			http.Error(w, "bad code", 400)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(userinfo)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	o2 := &auth.GenericOAuth{
		Cfg: &oauth2.Config{
			ClientID: "cid", ClientSecret: "csec",
			RedirectURL: "https://kuso.example/api/auth/oauth2/callback",
			Endpoint:    oauth2.Endpoint{AuthURL: srv.URL + "/auth", TokenURL: srv.URL + "/token"},
		},
		UserURL: srv.URL + "/userinfo",
	}
	h := &handlers.OAuthHandler{DB: d, Issuer: iss, OAuth2: o2, Logger: slog.Default()}
	r := chi.NewRouter()
	h.MountPublic(r)
	return r, d, code
}

// driveOAuth2 runs the generic /api/auth/oauth2 → IdP → callback roundtrip.
func driveOAuth2(t *testing.T, r http.Handler, code string) *httptest.ResponseRecorder {
	t.Helper()
	startReq := httptest.NewRequest(http.MethodGet, "/api/auth/oauth2", nil)
	startReq.Header.Set("X-Forwarded-Proto", "https")
	startRR := httptest.NewRecorder()
	r.ServeHTTP(startRR, startReq)
	if startRR.Code != http.StatusFound {
		t.Fatalf("oauth2 start: %d body=%q", startRR.Code, startRR.Body.String())
	}
	loc, _ := url.Parse(startRR.Header().Get("Location"))
	state := loc.Query().Get("state")
	cbReq := httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/auth/oauth2/callback?code=%s&state=%s",
			url.QueryEscape(code), url.QueryEscape(state)), nil)
	cbReq.Header.Set("X-Forwarded-Proto", "https")
	for _, c := range startRR.Result().Cookies() {
		cbReq.AddCookie(c)
	}
	cbRR := httptest.NewRecorder()
	r.ServeHTTP(cbRR, cbReq)
	return cbRR
}

// An unlinked stub-password account whose username collides with a
// generic-oauth2 login must NOT be auto-linked. Legacy builds created
// ALL oauth users (GitHub included) as provider='local'/providerId=NULL
// with a stub password, so "stub + unlinked" is NOT proof the account
// came from this oauth2 IdP. Auto-linking on username alone here would
// be cross-provider account takeover on any self-registering IdP.
func TestOAuth2_StubAccountUsernameCollision_NotAutoLinked(t *testing.T) {
	r, d, code := newOAuth2Harness(t, map[string]any{
		"sub": "idp-subject-9000", "preferred_username": "alice", "email": "alice@idp.example",
	})
	ctx := context.Background()
	stub, _ := auth.HashPassword("throwaway", auth.StubPasswordCost)
	if err := d.CreateUser(ctx, db.CreateUserInput{
		ID: "u-alice", Username: "alice", Email: "alice-existing@example.com",
		PasswordHash: stub, IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rr := driveOAuth2(t, r, code)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status: %d (want 409) body=%q", rr.Code, rr.Body.String())
	}
	if jwtCookie(rr) != nil {
		t.Error("session cookie issued for unproven oauth2 username collision")
	}
	u, err := d.FindUserByID(ctx, "u-alice")
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if u.ProviderID.Valid && u.ProviderID.String != "" {
		t.Errorf("account got silently linked: providerId=%q", u.ProviderID.String)
	}
}

// A stub-password account with NO GithubUserLink proof must not be
// adopted by a colliding GitHub identity — GitHub usernames are
// publicly registrable, so the username match alone proves nothing.
func TestOAuth_StubAccountWithoutLink_Rejected(t *testing.T) {
	r, _, gm, d := newOAuthHarness(t)
	ctx := context.Background()
	stub, _ := auth.HashPassword("throwaway", auth.StubPasswordCost)
	if err := d.CreateUser(ctx, db.CreateUserInput{
		ID: "u-idp", Username: "octocat", Email: "idp-user@example.com",
		PasswordHash: stub, IsActive: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rr := drive(t, r, gm)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status: %d (want 409) body=%q", rr.Code, rr.Body.String())
	}
	if jwtCookie(rr) != nil {
		t.Error("session cookie issued for unproven stub-account collision")
	}
}

// --- disabled accounts (S-review Finding 8) ----------------------------

func TestOAuth_DisabledUser_Unauthorized(t *testing.T) {
	r, _, gm, d := newOAuthHarness(t)
	ctx := context.Background()
	stub, _ := auth.HashPassword("throwaway", auth.StubPasswordCost)
	if err := d.CreateOAuthUser(ctx, db.CreateOAuthUserInput{
		ID: "u-off", Username: "octocat", Email: "octocat@github.example",
		PasswordHash: stub, Provider: "github", ProviderID: "424242",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	off := false
	if err := d.UpdateUser(ctx, "u-off", db.UpdateUserInput{IsActive: &off}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	rr := drive(t, r, gm)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status: %d (want 401) body=%q", rr.Code, rr.Body.String())
	}
	if jwtCookie(rr) != nil {
		t.Error("session cookie issued for a disabled account")
	}
}

// --- invite flow must not bootstrap-promote (S-review Finding 2) -------

func TestOAuth_InviteRedemption_DoesNotPromoteToAdmin(t *testing.T) {
	r, h, gm, d := newOAuthHarness(t)
	ctx := context.Background()

	// A viewer-level invite into a team group, on a cluster with NO
	// admin — exactly the disaster-recovery shape that used to promote
	// the invitee to admin before honoring the invite.
	if err := d.CreateGroup(ctx, "grp-team", "team", "test group"); err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := d.SetGroupTenancy(ctx, "grp-team", db.GroupTenancy{InstanceRole: db.InstanceRoleViewer}); err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	role := "viewer"
	gid := "grp-team"
	if err := d.CreateInvite(ctx, db.CreateInviteInput{
		ID: "inv-1", Token: "invite-token-1", GroupID: &gid, InstanceRole: &role,
		CreatedBy: "admin", MaxUses: 1,
	}); err != nil {
		t.Fatalf("invite: %v", err)
	}

	rr := driveWithCookies(t, r, gm, &http.Cookie{Name: "kuso_invite_token", Value: "invite-token-1"})
	if rr.Code != http.StatusFound {
		t.Fatalf("callback: %d %q", rr.Code, rr.Body.String())
	}
	c := jwtCookie(rr)
	if c == nil {
		t.Fatal("no session cookie")
	}
	claims, err := h.Issuer.Verify(c.Value)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	groups, err := d.UserGroupNames(ctx, claims.UserID)
	if err != nil {
		t.Fatalf("groups: %v", err)
	}
	for _, g := range groups {
		if g == "kuso-admins" {
			t.Fatalf("invitee was bootstrap-promoted to admin: groups=%v", groups)
		}
	}
	found := false
	for _, g := range groups {
		if g == "team" {
			found = true
		}
	}
	if !found {
		t.Errorf("invitee not attached to the invite's group: %v", groups)
	}
	ten, err := d.ListUserTenancy(ctx, claims.UserID)
	if err != nil {
		t.Fatalf("tenancy: %v", err)
	}
	if ten.InstanceRole != db.InstanceRoleViewer {
		t.Errorf("instance role: %q (want viewer)", ten.InstanceRole)
	}
	inv, err := d.FindInviteByToken(ctx, "invite-token-1")
	if err != nil {
		t.Fatalf("re-read invite: %v", err)
	}
	if inv.UsedCount != 1 {
		t.Errorf("usedCount: %d (want 1)", inv.UsedCount)
	}
}
