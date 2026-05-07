// Tenancy cache — wraps ListUserTenancy with a per-user, TTL-bound
// cache so the authorize path doesn't issue a JOIN against
// "_UserToUserGroup" on every request.
//
// Why this exists:
//   The frontend polls aggressively (canvas 5s, services 10s, builds
//   3s during a build). Every authorized non-admin request re-runs
//   the tenancy join. Twenty active project users ≈ 10 SELECTs/sec
//   on the same index lookup, fighting for a 25-conn pool.
//
// Correctness:
//   - 60s TTL is well below the JWT expiry; tokens themselves are
//     revoked through the watermark path, which we don't bypass —
//     the auth middleware still consults UserTokenWatermark on every
//     request. The cache only memoises which projects a user has
//     access to, given they already have a valid token.
//   - When this process performs InvalidateUser*Tokens it bumps a
//     local generation counter and evicts the user from the cache.
//     Other replicas will pierce within 60s naturally — same window
//     as the existing watermark consistency model.
//   - Empty-tenancy results are cached too (so a user with no groups
//     doesn't re-issue the JOIN per request).
package db

import (
	"context"
	"sync"
	"time"
)

// tenancyCacheTTL is how long a per-user tenancy result is reused
// without re-fetching. Longer = more pool relief; shorter = faster
// propagation of group-membership changes across replicas. 60s
// matches the existing watermark/jwt-mint cadence.
const tenancyCacheTTL = 60 * time.Second

type tenancyCacheEntry struct {
	tenancy GroupTenancy
	storedAt time.Time
}

type tenancyCache struct {
	mu      sync.RWMutex
	entries map[string]tenancyCacheEntry
}

func newTenancyCache() *tenancyCache {
	return &tenancyCache{entries: map[string]tenancyCacheEntry{}}
}

func (c *tenancyCache) get(userID string) (GroupTenancy, bool) {
	if c == nil || userID == "" {
		return GroupTenancy{}, false
	}
	c.mu.RLock()
	e, ok := c.entries[userID]
	c.mu.RUnlock()
	if !ok {
		return GroupTenancy{}, false
	}
	if time.Since(e.storedAt) > tenancyCacheTTL {
		return GroupTenancy{}, false
	}
	return e.tenancy, true
}

func (c *tenancyCache) put(userID string, t GroupTenancy) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	c.entries[userID] = tenancyCacheEntry{tenancy: t, storedAt: time.Now()}
	c.mu.Unlock()
}

func (c *tenancyCache) evict(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, userID)
	c.mu.Unlock()
}

func (c *tenancyCache) evictAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]tenancyCacheEntry{}
	c.mu.Unlock()
}

// ListUserTenancyCached returns the user's tenancy from the in-process
// cache when fresh, otherwise from the underlying join. Safe to call
// concurrently. Falls back to the live query if the cache pointer
// isn't initialised yet (test fixtures).
func (d *DB) ListUserTenancyCached(ctx context.Context, userID string) (GroupTenancy, error) {
	if d == nil {
		return GroupTenancy{}, nil
	}
	if d.tenancy != nil {
		if t, ok := d.tenancy.get(userID); ok {
			return t, nil
		}
	}
	t, err := d.ListUserTenancy(ctx, userID)
	if err != nil {
		return GroupTenancy{}, err
	}
	if d.tenancy != nil {
		d.tenancy.put(userID, t)
	}
	return t, nil
}

// EvictUserTenancy drops the cached entry for one user. Called from
// the same handlers that bump UserTokenInvalidation, so a role/group
// change applied on this replica shows up immediately.
func (d *DB) EvictUserTenancy(userID string) {
	if d == nil || d.tenancy == nil {
		return
	}
	d.tenancy.evict(userID)
}

// EvictAllTenancy drops every cached entry. Used by group/role bulk
// invalidations (InvalidateUsersByRole, InvalidateUsersByGroup) where
// listing the affected users would itself be a JOIN.
func (d *DB) EvictAllTenancy() {
	if d == nil || d.tenancy == nil {
		return
	}
	d.tenancy.evictAll()
}
