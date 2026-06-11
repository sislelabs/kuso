package auth

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// ctxKey is unexported so callers must go through ClaimsFromContext.
type ctxKey int

const claimsCtxKey ctxKey = 0

// RevocationChecker, when set on an Issuer, is consulted on every
// successful Verify in the middleware. Used to wire the RevokedToken
// DB lookups without making the auth package depend on db.
//
// SECURITY: the canonical implementation fails CLOSED (returns true
// = treat as revoked) on cache miss + DB error. Fail-open would let
// a transient DB outage silently un-revoke every previously revoked
// token, which is the worst-case outcome for a token-revocation
// surface. The previous comment claimed fail-open; the cmd/kuso-
// server/revocation.go implementation has always been fail-closed.
// Don't write a new RevocationChecker that returns false on DB
// errors — you'll undo the security property the caller relies on.
//
// The checker receives both the jti AND the userID/iat so it can
// query both the per-jti RevokedToken table and the per-user
// UserTokenInvalidation watermark in a single hop.
type RevocationChecker func(ctx context.Context, jti, userID string, iat time.Time) bool

// SetRevocationChecker installs the per-request revocation hook. Pass
// nil to disable. Safe to call once at startup; not safe to mutate
// concurrently with in-flight requests.
func (i *Issuer) SetRevocationChecker(fn RevocationChecker) {
	i.revoked = fn
}

// Middleware returns an http.Handler middleware that pulls the bearer
// token from Authorization, verifies it, and stuffs the *Claims into the
// request context. Requests without a token, or with an invalid token,
// receive 401 — except for the routes in skip, which pass through.
//
// We intentionally don't read tokens from cookies or query strings; the
// TS server only accepts Authorization: Bearer, and matching keeps the
// surface tight.
func (i *Issuer) Middleware(skip ...string) func(http.Handler) http.Handler {
	skipSet := make(map[string]struct{}, len(skip))
	for _, p := range skip {
		skipSet[p] = struct{}{}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skipSet[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			tok, ok := bearerToken(r)
			if !ok {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			claims, err := i.Verify(tok)
			if err != nil {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			// Revocation check after signature/expiry. Two probes
			// (per-jti RevokedToken + per-user invalidation
			// watermark) folded into one hook so the middleware
			// doesn't grow a DB pool reference. The checker fails
			// CLOSED on error / cache miss (returns "revoked" → 401)
			// — a transient DB outage must NOT silently un-revoke a
			// previously revoked token. See the RevocationChecker type
			// doc above and cmd/kuso-server/revocation.go.
			if i.CheckRevoked(r.Context(), claims) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), claimsCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CheckRevoked reports whether the given verified claims have been
// revoked, consulting the same per-jti + per-user hook the Middleware
// uses. Returns false (not revoked) when no checker is installed.
//
// Handlers mounted on the PUBLIC router that verify tokens themselves
// (the WebSocket upgraders) MUST call this after Verify — Verify only
// checks signature + expiry, so without this a logged-out / deactivated
// principal's still-unexpired token keeps working on those surfaces.
// Fails CLOSED on DB error/cache miss, exactly like the middleware path.
func (i *Issuer) CheckRevoked(ctx context.Context, c *Claims) bool {
	if i.revoked == nil || c == nil {
		return false
	}
	var iat time.Time
	if c.IssuedAt != nil {
		iat = c.IssuedAt.Time
	}
	return i.revoked(ctx, c.ID, c.UserID, iat)
}

// ClaimsFromContext returns the verified Claims previously stored by
// Middleware, or nil + false if the request was unauthenticated.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey).(*Claims)
	return c, ok
}

// ContextWithClaims stores already-verified claims under the same key
// Middleware uses, so downstream code reading ClaimsFromContext works.
// For handlers on the PUBLIC router that verify the token themselves
// (the WebSocket upgraders) — these MUST have called Verify and
// CheckRevoked first; this helper does no validation of its own.
func ContextWithClaims(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey, c)
}

// WithClaimsForTest stuffs claims into ctx using the same key the
// Middleware would, so tests can short-circuit JWT verification when
// they only want to exercise a handler. Production code MUST go
// through Middleware.
func WithClaimsForTest(ctx context.Context, c *Claims) context.Context {
	return ContextWithClaims(ctx, c)
}

// bearerToken pulls a token out of "Authorization: Bearer <token>"
// or, failing that, the kuso.JWT_TOKEN HttpOnly cookie. Both forms
// must hit the same verify path so the SPA (cookie) and the CLI
// (Bearer header) share state.
func bearerToken(r *http.Request) (string, bool) {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if strings.HasPrefix(h, prefix) {
			if t := strings.TrimSpace(h[len(prefix):]); t != "" {
				return t, true
			}
		}
	}
	if c, err := r.Cookie("kuso.JWT_TOKEN"); err == nil && c.Value != "" {
		return c.Value, true
	}
	return "", false
}
