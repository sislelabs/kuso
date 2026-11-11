package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kuso/server/internal/auth"
)

// SEC-6 (2026-11-10): a script on an addon console page (same origin)
// could open /ws/ routes on the viewer's session cookie. Browser upgrades
// now need a one-time ticket bound to the path; the CLI keeps bearer.
func TestAuthenticateWS(t *testing.T) {
	iss, err := auth.NewIssuer("test-secret", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	claims := auth.Claims{UserID: "u1", Username: "ivo"}
	session, err := iss.Sign(claims)
	if err != nil {
		t.Fatal(err)
	}
	const path = "/ws/projects/p/services/s/terminal"
	upgrade := func(mut func(*http.Request)) (int, string) {
		r := httptest.NewRequest(http.MethodGet, path+"?env=production", nil)
		mut(r)
		w := httptest.NewRecorder()
		_, hdr, ok := authenticateWS(w, r, iss)
		if !ok {
			return w.Code, ""
		}
		return http.StatusOK, hdr.Get("Sec-WebSocket-Protocol")
	}
	cookie := func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "kuso.JWT_TOKEN", Value: session}) }

	if code, _ := upgrade(func(r *http.Request) { cookie(r); r.Header.Set("Origin", "https://kuso.example.com") }); code != http.StatusUnauthorized {
		t.Errorf("browser cookie upgrade: %d, want 401", code)
	}
	if code, _ := upgrade(cookie); code != http.StatusOK {
		t.Errorf("non-browser cookie upgrade: %d, want 200", code)
	}
	if code, proto := upgrade(func(r *http.Request) {
		r.Header.Set("Sec-WebSocket-Protocol", "kuso.bearer, "+session)
	}); code != http.StatusOK || proto != "kuso.bearer" {
		t.Errorf("bearer upgrade: %d proto=%q", code, proto)
	}

	ticket, err := iss.SignWSTicket(claims, path)
	if err != nil {
		t.Fatal(err)
	}
	withTicket := func(tk string) func(*http.Request) {
		return func(r *http.Request) {
			cookie(r)
			r.Header.Set("Origin", "https://kuso.example.com")
			r.Header.Set("Sec-WebSocket-Protocol", "kuso.ticket, "+tk)
		}
	}
	if code, proto := upgrade(withTicket(ticket)); code != http.StatusOK || proto != "kuso.ticket" {
		t.Errorf("ticket upgrade: %d proto=%q", code, proto)
	}
	if code, _ := upgrade(withTicket(ticket)); code != http.StatusUnauthorized {
		t.Errorf("replayed ticket: %d, want 401", code)
	}
	other, _ := iss.SignWSTicket(claims, "/ws/projects/p/services/s/logs")
	if code, _ := upgrade(withTicket(other)); code != http.StatusUnauthorized {
		t.Errorf("ticket for another path: %d, want 401", code)
	}
	if code, _ := upgrade(withTicket(session)); code != http.StatusUnauthorized {
		t.Errorf("session JWT presented as a ticket: %d, want 401", code)
	}
}
