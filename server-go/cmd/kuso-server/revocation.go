package main

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// revocationCacheTTL bounds how long a last-known-good answer is
// trusted when the DB query errors. Short enough that a real revoke
// takes effect within 30s of Postgres recovering; long enough that a
// brief pool blip doesn't 401 every active user.
const revocationCacheTTL = 30 * time.Second

// revocationCache memoises the per-request answer when we have to
// fall back during a DB outage. Two maps so per-jti and per-user
// lookups don't contend; both are tiny and bounded by active session
// count.
type revocationCache struct {
	mu        sync.RWMutex
	jti       map[string]cachedBool
	watermark map[string]cachedTime
}

type cachedBool struct {
	v     bool
	until time.Time
}

type cachedTime struct {
	v     time.Time
	until time.Time
}

func newRevocationCache() *revocationCache {
	return &revocationCache{
		jti:       map[string]cachedBool{},
		watermark: map[string]cachedTime{},
	}
}

func (c *revocationCache) getJTI(k string) (bool, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.jti[k]
	if !ok || time.Now().After(e.until) {
		return false, false
	}
	return e.v, true
}

func (c *revocationCache) putJTI(k string, v bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jti[k] = cachedBool{v: v, until: time.Now().Add(revocationCacheTTL)}
}

func (c *revocationCache) getWatermark(k string) (time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.watermark[k]
	if !ok || time.Now().After(e.until) {
		return time.Time{}, false
	}
	return e.v, true
}

func (c *revocationCache) putWatermark(k string, v time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.watermark[k] = cachedTime{v: v, until: time.Now().Add(revocationCacheTTL)}
}

// makeRevocationChecker returns the closure wired into the auth
// middleware. Fails closed on DB error after the cache window expires
// — see the comment in main.go where this is installed.
func makeRevocationChecker(database *db.DB) auth.RevocationChecker {
	cache := newRevocationCache()
	return func(ctx context.Context, jti, userID string, iat time.Time) bool {
		// Per-jti check.
		if jti != "" {
			revoked, err := database.IsTokenRevoked(ctx, jti)
			if err != nil {
				if cached, ok := cache.getJTI(jti); ok {
					if cached {
						return true
					}
					// fall through to the watermark check below
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
		// Per-user watermark check.
		if userID != "" && !iat.IsZero() {
			watermark, err := database.UserTokenWatermark(ctx, userID)
			if err != nil {
				if cached, ok := cache.getWatermark(userID); ok {
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
		return false
	}
}
