package http

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"kuso/server/internal/auth"
)

func serveEnvelope(t *testing.T, h http.Handler, req *http.Request) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("%s %s: content-type %q, body %q", req.Method, req.URL.Path, ct, rr.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("%s %s: body not JSON: %v (%q)", req.Method, req.URL.Path, err, rr.Body.String())
	}
	return rr, m
}

// TestUnknownAPIRoute_JSON404 — a typo'd /api path answers a JSON 404 for
// every method (a POST used to hit the SPA's plain-text 405), carrying the
// same request id as the X-Request-Id header.
func TestUnknownAPIRoute_JSON404(t *testing.T) {
	t.Parallel()
	iss, _ := auth.NewIssuer("test-secret", 0)
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		rr, body := serveEnvelope(t, r, httptest.NewRequest(m, "/api/projectz", strings.NewReader(`{}`)))
		if rr.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", m, rr.Code)
		}
		if body["code"] != "not_found" {
			t.Errorf("%s: code %v", m, body["code"])
		}
		id := rr.Header().Get("X-Request-Id")
		if id == "" || body["requestId"] != id {
			t.Errorf("%s: header id %q, body requestId %v", m, id, body["requestId"])
		}
	}
}

// TestKnownAPIRoute_WrongMethod_JSON405 — the path exists, the method doesn't.
func TestKnownAPIRoute_WrongMethod_JSON405(t *testing.T) {
	t.Parallel()
	iss, _ := auth.NewIssuer("test-secret", 0)
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})
	rr, body := serveEnvelope(t, r, httptest.NewRequest(http.MethodDelete, "/api/auth/session", nil))
	if rr.Code != http.StatusMethodNotAllowed || body["code"] != "method_not_allowed" {
		t.Errorf("status %d body %v, want 405 method_not_allowed", rr.Code, body)
	}
}

// TestAuthMiddleware_JSON401 — the bearer middleware answers in the
// envelope and says why: no token, an expired one, or a bad one.
func TestAuthMiddleware_JSON401(t *testing.T) {
	t.Parallel()
	const secret = "test-secret"
	iss, _ := auth.NewIssuer(secret, 0)
	r := NewRouter(Deps{Issuer: iss, Logger: slog.Default()})

	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, auth.Claims{
		UserID:           "u",
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))},
	}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ auth, want string }{
		{"", "missing bearer token"},
		{"Bearer " + expired, "token expired"},
		{"Bearer not-a-jwt", "invalid token"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		rr, body := serveEnvelope(t, r, req)
		if rr.Code != http.StatusUnauthorized || body["code"] != "unauthorized" {
			t.Errorf("%q: status %d body %v", tc.want, rr.Code, body)
		}
		if msg, _ := body["error"].(string); !strings.Contains(msg, tc.want) {
			t.Errorf("error %q, want it to contain %q", msg, tc.want)
		}
	}
}
