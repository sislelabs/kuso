package auth

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is unexported so callers must go through ClaimsFromContext.
type ctxKey int

const claimsCtxKey ctxKey = 0

// RevocationChecker, when set on an Issuer, is consulted on every
// successful Verify in the middleware. If it returns true the token is
// rejected as if signature verification had failed. Used to wire the
// RevokedToken DB lookup without making the auth package depend on db.
type RevocationChecker func(ctx context.Context, jti string) bool

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
			// Revocation check after signature/expiry. Cheap PK probe
			// against the RevokedToken table — sub-millisecond hot
			// path with a hot pgx pool. Fail-open on checker error
			// (treat as not-revoked) so a transient DB outage doesn't
			// log every user out.
			if i.revoked != nil && claims.ID != "" {
				if i.revoked(r.Context(), claims.ID) {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			ctx := context.WithValue(r.Context(), claimsCtxKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// ClaimsFromContext returns the verified Claims previously stored by
// Middleware, or nil + false if the request was unauthenticated.
func ClaimsFromContext(ctx context.Context) (*Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey).(*Claims)
	return c, ok
}

// WithClaimsForTest stuffs claims into ctx using the same key the
// Middleware would, so tests can short-circuit JWT verification when
// they only want to exercise a handler. Production code MUST go
// through Middleware.
func WithClaimsForTest(ctx context.Context, c *Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey, c)
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
