package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"kuso/server/internal/auth"
)

// revocationStore is the slice of the DB the revocation checker needs.
// Narrowed to an interface so the read-through logic is unit-testable
// without a live Postgres.
type revocationStore interface {
	IsTokenRevoked(ctx context.Context, jti string) (bool, error)
	UserTokenWatermark(ctx context.Context, userID string) (time.Time, error)
}

// revocationFreshTTL is the happy-path read-through window. Within it a
// cached "not revoked" answer is served WITHOUT touching Postgres, so
// steady-state auth adds ~zero DB QPS instead of two queries per request.
// A revoke therefore takes effect within this window in the worst case —
// the same staleness a short cache always implies. Positive (revoked)
// results are authoritative and cached too, so a revoke that HAS been read
// keeps blocking regardless of the DB's state.
const revocationFreshTTL = 10 * time.Second

// revocationOutageTTL bounds how long a last-known-good answer survives
// once the DB starts erroring. Longer than the fresh window: a multi-
// minute Postgres restart must not 401 every active user and bounce them
// to /login for reads that live entirely in kube CRs. After it expires we
// fail closed (unknown tokens are treated as revoked).
const revocationOutageTTL = 5 * time.Minute

// revocationCache memoises the per-request answer read-through: an entry
// is FRESH (serve without a DB hit) until freshUntil, then STALE-usable
// (serve only while the DB is erroring) until staleUntil, then gone. Two
// maps so per-jti and per-user lookups don't contend; both are tiny and
// bounded by active session count.
type revocationCache struct {
	mu        sync.RWMutex
	jti       map[string]cachedBool
	watermark map[string]cachedTime
}

type cachedBool struct {
	v          bool
	freshUntil time.Time
	staleUntil time.Time
}

type cachedTime struct {
	v          time.Time
	freshUntil time.Time
	staleUntil time.Time
}

// cacheState is the outcome of a cache lookup.
type cacheState int

const (
	cacheMiss  cacheState = iota // not present or fully expired
	cacheFresh                   // within the read-through window; skip the DB
	cacheStale                   // past fresh but usable as an outage fallback
)

func newRevocationCache() *revocationCache {
	return &revocationCache{
		jti:       map[string]cachedBool{},
		watermark: map[string]cachedTime{},
	}
}

func (c *revocationCache) getJTI(k string) (bool, cacheState) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.jti[k]
	if !ok {
		return false, cacheMiss
	}
	now := time.Now()
	switch {
	case now.Before(e.freshUntil):
		return e.v, cacheFresh
	case now.Before(e.staleUntil):
		return e.v, cacheStale
	default:
		return false, cacheMiss
	}
}

func (c *revocationCache) putJTI(k string, v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.jti[k] = cachedBool{v: v, freshUntil: now.Add(revocationFreshTTL), staleUntil: now.Add(revocationOutageTTL)}
}

func (c *revocationCache) getWatermark(k string) (time.Time, cacheState) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.watermark[k]
	if !ok {
		return time.Time{}, cacheMiss
	}
	now := time.Now()
	switch {
	case now.Before(e.freshUntil):
		return e.v, cacheFresh
	case now.Before(e.staleUntil):
		return e.v, cacheStale
	default:
		return time.Time{}, cacheMiss
	}
}

func (c *revocationCache) putWatermark(k string, v time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	c.watermark[k] = cachedTime{v: v, freshUntil: now.Add(revocationFreshTTL), staleUntil: now.Add(revocationOutageTTL)}
}

// makeRevocationChecker returns the closure wired into the auth
// middleware. It is READ-THROUGH: a fresh cached answer short-circuits the
// DB entirely (steady-state auth adds ~zero DB QPS), the DB is queried only
// on a cache miss / expiry, and a stale entry is the outage fallback. Fails
// closed once even the stale window expires — see the comment in main.go
// where this is installed.
func makeRevocationChecker(database revocationStore) auth.RevocationChecker {
	cache := newRevocationCache()
	return func(ctx context.Context, jti, userID string, iat time.Time) bool {
		// Per-jti check.
		if jti != "" {
			if cached, state := cache.getJTI(jti); state == cacheFresh {
				// Read-through hit: trust it without a DB round-trip. A
				// cached revoke stays authoritative; a cached non-revoke
				// still falls through to the watermark check below.
				if cached {
					return true
				}
			} else {
				revoked, err := database.IsTokenRevoked(ctx, jti)
				if err != nil {
					// DB error: fall back to a stale entry if we have one,
					// else fail closed.
					if cached, s := cache.getJTI(jti); s != cacheMiss {
						if cached {
							return true
						}
					} else {
						slog.Default().Warn("revocation: jti probe failed, no cache; failing closed",
							"jti", jti, "err", err)
						return true
					}
				} else {
					cache.putJTI(jti, revoked)
					if revoked {
						return true
					}
				}
			}
		}
		// Per-user watermark check.
		if userID != "" && !iat.IsZero() {
			if cached, state := cache.getWatermark(userID); state == cacheFresh {
				if !cached.IsZero() && iat.Before(cached) {
					return true
				}
			} else {
				watermark, err := database.UserTokenWatermark(ctx, userID)
				if err != nil {
					if cached, s := cache.getWatermark(userID); s != cacheMiss {
						if !cached.IsZero() && iat.Before(cached) {
							return true
						}
					} else {
						slog.Default().Warn("revocation: watermark probe failed, no cache; failing closed",
							"user", userID, "err", err)
						return true
					}
				} else {
					cache.putWatermark(userID, watermark)
					if !watermark.IsZero() && iat.Before(watermark) {
						return true
					}
				}
			}
		}
		return false
	}
}
