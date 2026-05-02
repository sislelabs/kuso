package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMiddleware_AcceptsValidBearer(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	tok, _ := iss.Sign(Claims{UserID: "u1", Username: "u1", Role: "admin"})

	var seen *Claims
	h := iss.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		c, ok := ClaimsFromContext(r.Context())
		if !ok {
			t.Fatal("claims missing from context")
		}
		seen = c
	}))

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	if seen == nil || seen.UserID != "u1" {
		t.Errorf("seen claims: %+v", seen)
	}
}

func TestMiddleware_Rejects(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	h := iss.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := map[string]string{
		"missing":   "",
		"not bearer": "Basic dXNlcjpwYXNz",
		"empty token": "Bearer ",
		"garbage":     "Bearer not-a-jwt",
	}
	for name, hdr := range cases {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/protected", nil)
			if hdr != "" {
				req.Header.Set("Authorization", hdr)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("%s: got %d, want 401", name, rr.Code)
			}
		})
	}
}

func TestMiddleware_SkipsListedPath(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	called := false
	h := iss.Middleware("/healthz")(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || !called {
		t.Errorf("skipped path was blocked: code=%d called=%v", rr.Code, called)
	}
}
