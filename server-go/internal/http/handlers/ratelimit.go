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
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// rateLimitMax / rateLimitWindow define the per-bucket cap that gates
// the login + invite-redeem + review + bootstrap + OAuth-start paths.
// Each surface has its own bucket (key prefix in LoginAttempt."ip").
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

// Login gets two buckets: (ip, username) at rateLimitMax so a password
// guesser is capped per account, plus a looser per-IP cap so one IP
// can't spray unlimited usernames. Keying on the pair means one noisy
// source (or a proxy we failed to see through) can no longer lock
// every user out of /login.
const loginPerIPMax = 60

// allowAttemptFn is the bucket check; swapped in tests so the keying
// can be exercised without Postgres.
var allowAttemptFn = allowAttempt

// allowAttempt returns (true, 0) when key is under max, (false,
// retryAfter) when over. Fails open on DB error so a Postgres outage
// doesn't lock the whole login surface.
func allowAttempt(ctx context.Context, key string, max int) (bool, time.Duration) {
	limiterDBMu.RLock()
	d := limiterDB
	limiterDBMu.RUnlock()
	if d == nil {
		return true, 0
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	res, err := d.AllowLoginAttempt(ctx, key, max, rateLimitWindow)
	if err != nil {
		// Fail open — see package doc.
		return true, 0
	}
	return res.Allowed, res.RetryAfter
}

func writeTooMany(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", itoaShort(seconds))
	writeErr(w, http.StatusTooManyRequests, "too many requests")
}

// withRateLimit wraps the handler with a per-IP bucket in its own
// scope, so exhausting one public surface (e.g. review-token probing)
// doesn't 429 the others.
func withRateLimit(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if ok, retryAfter := allowAttemptFn(r.Context(), scope+":"+clientIP(r), rateLimitMax); !ok {
			writeTooMany(w, retryAfter)
			return
		}
		next(w, r)
	}
}

// loginUsername peeks the JSON body for the username and restores the
// body for the handler. Unparseable bodies bucket under "" — the
// handler rejects them anyway.
func loginUsername(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return ""
	}
	var body struct {
		Username string `json:"username"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(body.Username))
}

// RateLimitedLogin gates the login endpoint with the per-(ip, username)
// and per-IP login buckets.
func RateLimitedLogin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if ok, retryAfter := allowAttemptFn(r.Context(), "login-ip:"+ip, loginPerIPMax); !ok {
			writeTooMany(w, retryAfter)
			return
		}
		if ok, retryAfter := allowAttemptFn(r.Context(), "login:"+ip+"|"+loginUsername(r), rateLimitMax); !ok {
			writeTooMany(w, retryAfter)
			return
		}
		next(w, r)
	}
}

// RateLimitedInvite is the wrapper for invite lookup/redemption.
func RateLimitedInvite(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit("invite", next)
}

// RateLimitedBootstrap is the wrapper for the node-bootstrap script and
// register endpoints.
func RateLimitedBootstrap(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit("bootstrap", next)
}

// RateLimitedReview gates the public, unauthenticated reviewer-page
// endpoints. The 32-char token is the only credential; without a limit
// an attacker could brute-force the token space at full request rate.
func RateLimitedReview(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit("review", next)
}

// RateLimitedOAuthStart caps OAuth-init requests. Without this, an
// attacker can abuse the start endpoint to burn through OAuthState
// rows / spam the Postgres write path / hit the upstream provider's
// rate limits with our IP.
func RateLimitedOAuthStart(next http.HandlerFunc) http.HandlerFunc {
	return withRateLimit("oauth", next)
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
