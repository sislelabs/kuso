// Package handlers — IP rate limiter for the auth surface.
//
// State lives in Postgres (LoginAttempt table). The previous in-
// process sync.Map version reset on every pod restart and didn't
// share state across replicas, so the effective cap was
// maxAttempts × replicas with a fresh window every roll. With the
// DB-backed limiter, restart and replica fan-out are no longer
// bypass vectors.
//
// The check is one atomic INSERT-on-conflict (see
// db.AllowLoginAttempt). If the DB is unreachable we fail-open
// rather than locking everyone out — the alternative is hard-
// failing /login on a transient DB blip, which is worse than
// briefly degrading the limiter.
package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// rateLimitMax / rateLimitWindow define the per-IP cap that gates
// the login + invite-redeem + OAuth-start paths. Centralised so the
// reset helper agrees with the live config.
const (
	rateLimitMax    = 10
	rateLimitWindow = 30 * time.Second
)

// limiterDB is the Postgres handle the limiter writes through. Set
// once at boot via SetRateLimiterDB; nil = fail-open (every request
// allowed) with a warning logged on first use.
var (
	limiterDBMu sync.RWMutex
	limiterDB   *db.DB
)

// SetRateLimiterDB wires the limiter to a Postgres handle. Call once
// during router construction. Idempotent so tests can swap handles.
func SetRateLimiterDB(d *db.DB) {
	limiterDBMu.Lock()
	limiterDB = d
	limiterDBMu.Unlock()
}

// ResetRateLimiterForTesting wipes the LoginAttempt table so a test
// run that shares a Postgres-backed test process doesn't trip the
// cap on the 11th login-flow test. Falls back to a no-op when no
// DB is wired (in-process unit tests).
func ResetRateLimiterForTesting() {
	limiterDBMu.RLock()
	d := limiterDB
	limiterDBMu.RUnlock()
	if d == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.ResetLoginAttemptsForTesting(ctx)
}

// allowAttempt is the shared check the wrapper helpers call. Returns
// (true, 0) when the IP is under the cap, (false, retryAfter) when
// over. Fails open on DB error so a Postgres outage doesn't lock the
// whole login surface.
func allowAttempt(ctx context.Context, ip string) (bool, time.Duration) {
	limiterDBMu.RLock()
	d := limiterDB
	limiterDBMu.RUnlock()
	if d == nil {
		return true, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := d.AllowLoginAttempt(ctx, ip, rateLimitMax, rateLimitWindow)
	if err != nil {
		// Fail open — see package doc.
		return true, 0
	}
	return res.Allowed, res.RetryAfter
}

// withRateLimit wraps the handler with the persistent limiter.
func withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		ok, retryAfter := allowAttempt(r.Context(), ip)
		if !ok {
			seconds := int(retryAfter.Seconds())
			if seconds < 1 {
				seconds = 1
			}
			w.Header().Set("Retry-After", itoaShort(seconds))
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

// RateLimitedLogin is the exported wrapper used by the router to gate
// the login endpoint. Same limiter governs the invite-redemption path
// (RateLimitedInvite) so an attacker can't bypass the cap by alternating
// between routes — both feed the same per-IP bucket.
func RateLimitedLogin(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit(next)
}

// RateLimitedInvite is the wrapper for invite redemption.
func RateLimitedInvite(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit(next)
}

// RateLimitedOAuthStart caps OAuth-init requests at the same rate as
// login. Without this, an attacker can abuse the start endpoint to
// burn through OAuthState rows / spam the Postgres write path / hit
// the upstream provider's rate limits with our IP. Same per-IP bucket
// as RateLimitedLogin so /api/auth/login + /api/auth/github + /api/
// auth/oauth2 all share the cap.
func RateLimitedOAuthStart(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit(next)
}

// itoaShort avoids strconv.Itoa to keep imports small. Inputs are
// always small positive seconds.
func itoaShort(n int) string {
	if n == 0 {
		return "0"
	}
	digits := make([]byte, 0, 4)
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
