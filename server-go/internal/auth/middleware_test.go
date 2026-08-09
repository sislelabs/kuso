package auth

import (
	"context"
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

// TestMiddleware_PermissionResolver: with a resolver installed, the
// baked JWT permissions are REPLACED by the live set — the core of
// per-request authority (a grant works on already-held tokens, a
// revoke stops working on them).
func TestMiddleware_PermissionResolver(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	// Token minted with a stale, over-privileged set.
	tok, _ := iss.Sign(Claims{UserID: "u1", Username: "u1", Permissions: []string{"settings:admin"}})

	iss.SetPermissionResolver(func(_ context.Context, c *Claims) ([]string, bool) {
		if c.UserID != "u1" {
			t.Errorf("resolver got user %q", c.UserID)
		}
		return []string{"projects:create"}, true
	})

	var seen *Claims
	h := iss.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = ClaimsFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d", rr.Code)
	}
	if seen == nil || len(seen.Permissions) != 1 || seen.Permissions[0] != "projects:create" {
		t.Errorf("permissions not replaced by resolver: %+v", seen)
	}
}

// TestMiddleware_PermissionResolverFailsClosed: resolver failure must
// EMPTY the permission set (still authenticated), never fall back to
// the baked claims — that fallback would resurrect a revoked admin's
// stale perms during exactly the DB-outage window that matters.
func TestMiddleware_PermissionResolverFailsClosed(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	tok, _ := iss.Sign(Claims{UserID: "u1", Username: "u1", Permissions: []string{"settings:admin"}})
	iss.SetPermissionResolver(func(_ context.Context, _ *Claims) ([]string, bool) {
		return nil, false
	})

	var seen *Claims
	h := iss.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = ClaimsFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: %d (request should stay authenticated)", rr.Code)
	}
	if seen == nil || len(seen.Permissions) != 0 {
		t.Errorf("failed resolution must empty perms, got %+v", seen)
	}
}

// TestMiddleware_NoResolverKeepsBakedPerms: nil resolver (tests,
// stripped wiring) preserves the pre-resolver behaviour exactly.
func TestMiddleware_NoResolverKeepsBakedPerms(t *testing.T) {
	t.Parallel()
	iss, _ := NewIssuer("s", time.Hour)
	tok, _ := iss.Sign(Claims{UserID: "u1", Username: "u1", Permissions: []string{"settings:admin"}})

	var seen *Claims
	h := iss.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen, _ = ClaimsFromContext(r.Context())
	}))
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if seen == nil || len(seen.Permissions) != 1 || seen.Permissions[0] != "settings:admin" {
		t.Errorf("baked perms should be preserved without resolver, got %+v", seen)
	}
}
