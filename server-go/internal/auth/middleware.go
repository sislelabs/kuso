package auth

import (
	"context"
	"net/http"
	"strings"
)

// ctxKey is unexported so callers must go through ClaimsFromContext.
type ctxKey int

const claimsCtxKey ctxKey = 0

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

// bearerToken pulls a token out of "Authorization: Bearer <token>".
// Falls back to false when the header is missing or malformed.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if h == "" {
		return "", false
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) {
		return "", false
	}
	t := strings.TrimSpace(h[len(prefix):])
	if t == "" {
		return "", false
	}
	return t, true
}
