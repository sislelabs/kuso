// Package handlers — minimal in-process IP rate limiter.
//
// We don't pull in a dependency for this — the auth surface is the only
// caller and the bucket is small (one entry per active source IP).
// State lives in process memory; it resets on restart, which is fine
// because (a) bcrypt makes brute force expensive enough that a 30s
// reset window doesn't help an attacker, and (b) a kuso instance
// restart is rare enough not to be a routine bypass vector.
//
// For multi-replica deployments later: swap the in-process bucket for
// a Redis-backed limiter. The interface stays the same.
package handlers

import (
	"net/http"
	"sync"
	"time"
)

// loginLimiter caps unauthenticated POST /api/auth/login + invite
// redemption to N attempts per window per IP. Excess attempts get a
// 429 with a Retry-After header.
type loginLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	max     int
	window  time.Duration
}

type ipBucket struct {
	count   int
	resetAt time.Time
}

func newLoginLimiter(max int, window time.Duration) *loginLimiter {
	return &loginLimiter{
		buckets: make(map[string]*ipBucket),
		max:     max,
		window:  window,
	}
}

// allow returns true when the IP is under the cap. It also lazily
// prunes stale entries, capped at one prune sweep per call to keep
// the constant factor small.
func (l *loginLimiter) allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[ip]
	if !ok || now.After(b.resetAt) {
		l.buckets[ip] = &ipBucket{count: 1, resetAt: now.Add(l.window)}
		// Opportunistic prune: at most one stale entry per call.
		for k, v := range l.buckets {
			if k != ip && now.After(v.resetAt) {
				delete(l.buckets, k)
				break
			}
		}
		return true, 0
	}
	if b.count >= l.max {
		return false, time.Until(b.resetAt)
	}
	b.count++
	return true, 0
}

// rateLimit wraps an http.HandlerFunc with the limiter. The IP key
// uses the same extractor the audit logger uses, but only when the
// request originates from a trusted reverse proxy (controlled by the
// KUSO_TRUSTED_PROXIES env so a direct caller can't spoof XFF).
//
// In keeping with the rest of the package we keep the limiter as a
// process-global; tests reset it via testResetLoginLimiter.
var defaultLoginLimiter = newLoginLimiter(10, 30*time.Second)

// withRateLimit wraps the handler with the default limiter.
func withRateLimit(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		ok, retryAfter := defaultLoginLimiter.allow(ip)
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
