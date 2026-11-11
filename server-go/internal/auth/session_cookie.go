package auth

import (
	"net/http"
	"strings"
	"time"
)

// Session cookie names (SEC-8). Tenant apps live on subdomains of the
// control-plane host (<svc>.<project>.<KUSO_DOMAIN>), so a tenant page
// can set a Domain=<KUSO_DOMAIN> cookie under the old name and fixate a
// visitor onto the attacker's session for chosen paths. A __Host- cookie
// can only be set by the control-plane host itself (Secure, Path=/, no
// Domain). It needs HTTPS, so plain-HTTP dev hosts keep the old name.
const (
	SessionCookieName       = "__Host-kuso_jwt"
	LegacySessionCookieName = "kuso.JWT_TOKEN"
)

// legacyCookieCutoff is when this server started. Over HTTPS the old
// cookie name is honoured only for tokens issued before then: sessions
// from before the upgrade keep working (and are moved to the new name on
// their next request), while a token planted under the old name since
// then is refused.
var legacyCookieCutoff = time.Now()

// RequestIsHTTPS reports whether the client reached kuso over TLS,
// directly or through the ingress.
func RequestIsHTTPS(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// SessionCookieToken returns the session token a browser sent, preferring
// the __Host- cookie, and whether it came from the legacy name.
func SessionCookieToken(r *http.Request) (tok string, legacy bool) {
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value, false
	}
	if c, err := r.Cookie(LegacySessionCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}

// LegacySessionAllowed reports whether a token read from the legacy
// cookie name may authenticate r (see legacyCookieCutoff).
func LegacySessionAllowed(r *http.Request, c *Claims) bool {
	if !RequestIsHTTPS(r) {
		return true
	}
	return c != nil && c.IssuedAt != nil && c.IssuedAt.Time.Before(legacyCookieCutoff)
}

// SetSessionCookie writes the session cookie under the name the scheme
// allows and expires the other one.
func SetSessionCookie(w http.ResponseWriter, r *http.Request, tok string, maxAge int) {
	secure := RequestIsHTTPS(r)
	name, other := LegacySessionCookieName, SessionCookieName
	if secure {
		name, other = SessionCookieName, LegacySessionCookieName
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: tok, Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: maxAge,
	})
	expireCookie(w, other, secure)
}

// ClearSessionCookies expires both session cookie names.
func ClearSessionCookies(w http.ResponseWriter, r *http.Request) {
	secure := RequestIsHTTPS(r)
	expireCookie(w, LegacySessionCookieName, secure)
	if secure {
		expireCookie(w, SessionCookieName, true)
	}
}

func expireCookie(w http.ResponseWriter, name string, secure bool) {
	// A __Host- cookie is only accepted (even to delete) with Secure.
	if strings.HasPrefix(name, "__Host-") && !secure {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/", HttpOnly: true, Secure: secure,
		SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
}

// sessionMaxAge is the cookie lifetime for a verified token: its
// remaining validity, or ten years for a token without exp.
func sessionMaxAge(c *Claims) int {
	if c == nil || c.ExpiresAt == nil {
		return 10 * 365 * 24 * 3600
	}
	if s := int(time.Until(c.ExpiresAt.Time).Seconds()); s > 0 {
		return s
	}
	return -1
}
