package auth

import (
	"context"
	"sync"
	"time"
)

// SeenRecorder is called by the Middleware after a request has fully
// authenticated (verified, not revoked). It runs inline on the request
// path, so implementations must return immediately — see
// NewThrottledSeenRecorder.
type SeenRecorder func(c *Claims)

// SetSeenRecorder installs the per-request "user was active" hook. Pass
// nil to disable. Safe to call once at startup; not safe to mutate
// concurrently with in-flight requests.
func (i *Issuer) SetSeenRecorder(fn SeenRecorder) {
	i.seen = fn
}

// SeenThrottle admits at most one event per key per interval. The map is
// bounded: once it reaches maxKeys, expired entries are pruned, and if
// that frees nothing the map is reset (worst case: one extra write per
// user, never unbounded memory).
type SeenThrottle struct {
	interval time.Duration
	maxKeys  int

	mu   sync.Mutex
	last map[string]time.Time
}

// NewSeenThrottle returns a throttle admitting one event per key per interval.
func NewSeenThrottle(interval time.Duration, maxKeys int) *SeenThrottle {
	if maxKeys < 1 {
		maxKeys = 1
	}
	return &SeenThrottle{interval: interval, maxKeys: maxKeys, last: make(map[string]time.Time)}
}

// Allow reports whether key may fire at now, and records it if so.
func (t *SeenThrottle) Allow(key string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.last[key]; ok && now.Sub(prev) < t.interval {
		return false
	}
	if _, ok := t.last[key]; !ok && len(t.last) >= t.maxKeys {
		for k, at := range t.last {
			if now.Sub(at) >= t.interval {
				delete(t.last, k)
			}
		}
		if len(t.last) >= t.maxKeys {
			t.last = make(map[string]time.Time)
		}
	}
	t.last[key] = now
	return true
}

// Len is the number of tracked keys (for tests).
func (t *SeenThrottle) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.last)
}

// NewThrottledSeenRecorder builds a SeenRecorder that writes at most once
// per user per interval, off the request goroutine, with its own short
// timeout so a slow DB never delays or fails the request. onErr may be nil.
func NewThrottledSeenRecorder(
	interval time.Duration,
	maxUsers int,
	write func(ctx context.Context, userID string, at time.Time) error,
	onErr func(userID string, err error),
) SeenRecorder {
	th := NewSeenThrottle(interval, maxUsers)
	return func(c *Claims) {
		if c == nil || c.UserID == "" {
			return
		}
		now := time.Now()
		if !th.Allow(c.UserID, now) {
			return
		}
		go func(userID string) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := write(ctx, userID, now); err != nil && onErr != nil {
				onErr(userID, err)
			}
		}(c.UserID)
	}
}
