package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// SEC-8: over HTTPS the session lives in a __Host- cookie a tenant
// subdomain can't set. The old name still works for sessions issued
// before this server started (and is moved to the new name), but a token
// planted under it since then is refused.
func TestMiddleware_SessionCookieNames(t *testing.T) {
	iss, _ := NewIssuer("s", time.Hour)
	old, _ := iss.Sign(Claims{UserID: "u1"})
	saved := legacyCookieCutoff
	t.Cleanup(func() { legacyCookieCutoff = saved })
	legacyCookieCutoff = time.Now().Add(2 * time.Second) // old was issued "before the upgrade"
	time.Sleep(10 * time.Millisecond)
	h := iss.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	do := func(name, tok string, https bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		if https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		req.AddCookie(&http.Cookie{Name: name, Value: tok})
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		return rr
	}

	rr := do(LegacySessionCookieName, old, true)
	if rr.Code != http.StatusOK {
		t.Fatalf("pre-upgrade session under the old name: %d", rr.Code)
	}
	var moved bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == SessionCookieName && c.Value == old && c.Secure && c.Path == "/" && c.Domain == "" {
			moved = true
		}
	}
	if !moved {
		t.Errorf("old-name session not moved to %s: %v", SessionCookieName, rr.Result().Cookies())
	}

	legacyCookieCutoff = time.Now().Add(-time.Hour) // planted after the upgrade
	if rr := do(LegacySessionCookieName, old, true); rr.Code != http.StatusUnauthorized {
		t.Errorf("post-upgrade token under the old name over HTTPS: %d, want 401", rr.Code)
	}
	if rr := do(LegacySessionCookieName, old, false); rr.Code != http.StatusOK {
		t.Errorf("old name on a plain-HTTP dev host: %d, want 200", rr.Code)
	}
	if rr := do(SessionCookieName, old, true); rr.Code != http.StatusOK {
		t.Errorf("__Host- session: %d, want 200", rr.Code)
	}
}

func TestSetSessionCookie_NameFollowsScheme(t *testing.T) {
	for _, https := range []bool{true, false} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
		if https {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
		rr := httptest.NewRecorder()
		SetSessionCookie(rr, req, "tok", 60)
		want := LegacySessionCookieName
		if https {
			want = SessionCookieName
		}
		var got string
		for _, c := range rr.Result().Cookies() {
			if c.Value == "tok" {
				got = c.Name
				if c.Secure != https || !c.HttpOnly {
					t.Errorf("https=%v: cookie %+v", https, c)
				}
			}
		}
		if got != want {
			t.Errorf("https=%v: session cookie %q, want %q", https, got, want)
		}
	}
}
