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

// tenancyRevPollInterval bounds how often a replica re-reads the
// shared "TenancyRev" row. In-process evictions apply instantly; this
// poll is what carries an edit made on ANOTHER replica, so it also
// bounds cross-replica revocation latency (~5s, vs the full 60s TTL
// when eviction was purely process-local). One indexed single-row
// SELECT per replica per 5s — noise next to the JOIN the cache saves.
const tenancyRevPollInterval = 5 * time.Second

type tenancyCacheEntry struct {
	tenancy  GroupTenancy
	storedAt time.Time
	rev      int64
}

// rolePermsEntry memoises UserPermissions (custom-role perms) with the
// same TTL as tenancy. Both feed the per-request permission resolver in
// the auth middleware, so they must stale-out together — a role change
// that shows in one but not the other would produce a mixed perms set.
type rolePermsEntry struct {
	perms    []string
	storedAt time.Time
	rev      int64
}

type tenancyCache struct {
	mu        sync.RWMutex
	entries   map[string]tenancyCacheEntry
	rolePerms map[string]rolePermsEntry
	// rev is the last "TenancyRev" value this replica observed;
	// revChecked is when. Entries are stamped with the rev the caller
	// observed BEFORE running its DB read (see revSnapshot), and a
	// stamp older than rev means a tenancy edit committed after the
	// entry's data was read — the entry misses. That closes the
	// evict-then-refill race the plain evictAll left open: a request
	// that read pre-edit rows could re-cache them AFTER the eviction
	// for a fresh 60s.
	rev        int64
	revChecked time.Time
}

func newTenancyCache() *tenancyCache {
	return &tenancyCache{
		entries:   map[string]tenancyCacheEntry{},
		rolePerms: map[string]rolePermsEntry{},
	}
}

func (c *tenancyCache) get(userID string) (GroupTenancy, bool) {
	if c == nil || userID == "" {
		return GroupTenancy{}, false
	}
	c.mu.RLock()
	e, ok := c.entries[userID]
	rev := c.rev
	c.mu.RUnlock()
	if !ok || e.rev != rev {
		return GroupTenancy{}, false
	}
	if time.Since(e.storedAt) > tenancyCacheTTL {
		return GroupTenancy{}, false
	}
	return e.tenancy, true
}

func (c *tenancyCache) put(userID string, t GroupTenancy, rev int64) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	c.entries[userID] = tenancyCacheEntry{tenancy: t, storedAt: time.Now(), rev: rev}
	c.mu.Unlock()
}

func (c *tenancyCache) evict(userID string) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, userID)
	delete(c.rolePerms, userID)
	c.mu.Unlock()
}

func (c *tenancyCache) evictAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = map[string]tenancyCacheEntry{}
	c.rolePerms = map[string]rolePermsEntry{}
	c.mu.Unlock()
}

func (c *tenancyCache) getPerms(userID string) ([]string, bool) {
	if c == nil || userID == "" {
		return nil, false
	}
	c.mu.RLock()
	e, ok := c.rolePerms[userID]
	rev := c.rev
	c.mu.RUnlock()
	if !ok || e.rev != rev || time.Since(e.storedAt) > tenancyCacheTTL {
		return nil, false
	}
	return e.perms, true
}

func (c *tenancyCache) putPerms(userID string, perms []string, rev int64) {
	if c == nil || userID == "" {
		return
	}
	c.mu.Lock()
	c.rolePerms[userID] = rolePermsEntry{perms: perms, storedAt: time.Now(), rev: rev}
	c.mu.Unlock()
}

// revSnapshot returns the rev to stamp a new entry with. Refreshing
// from the DB happens at most every tenancyRevPollInterval; on a
// changed rev the maps are dropped wholesale (stamp mismatch would
// age them out anyway — this frees the memory). read is the
// single-row SELECT; a read failure keeps the previous rev, which
// degrades exactly to the pre-rev behaviour (60s TTL bound).
func (c *tenancyCache) revSnapshot(read func() (int64, bool)) int64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	rev, fresh := c.rev, time.Since(c.revChecked) < tenancyRevPollInterval
	c.mu.RUnlock()
	if fresh {
		return rev
	}
	dbRev, ok := read()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revChecked = time.Now()
	if !ok {
		return c.rev
	}
	if dbRev != c.rev {
		c.rev = dbRev
		c.entries = map[string]tenancyCacheEntry{}
		c.rolePerms = map[string]rolePermsEntry{}
	}
	return c.rev
}

// bumpLocal advances the locally-known rev after this replica bumped
// the DB row itself, so its own next put doesn't stamp a stale rev.
func (c *tenancyCache) bumpLocal(rev int64) {
	if c == nil || rev == 0 {
		return
	}
	c.mu.Lock()
	if rev > c.rev {
		c.rev = rev
		c.revChecked = time.Now()
	}
	c.mu.Unlock()
}

// readTenancyRev fetches the shared revision counter. ok=false on any
// error — the caller keeps its previous rev (60s-TTL fallback).
func (d *DB) readTenancyRev(ctx context.Context) (int64, bool) {
	var rev int64
	err := d.QueryRowContext(ctx, `SELECT rev FROM "TenancyRev" WHERE id = 1`).Scan(&rev)
	if err != nil {
		return 0, false
	}
	return rev, true
}

// bumpTenancyRev advances the shared revision counter so OTHER
// replicas drop their tenancy caches on the next poll. Best-effort:
// a failed bump degrades to the 60s TTL bound, same as before the
// counter existed, so callers don't propagate the error. Must run
// AFTER the tenancy edit commits — entries are stamped with the rev
// observed before their data read, so "rev N visible ⇒ edits up to
// N visible" is what makes the stamping sound.
func (d *DB) bumpTenancyRev(ctx context.Context) {
	var rev int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO "TenancyRev" (id, rev) VALUES (1, 2)
		ON CONFLICT (id) DO UPDATE SET rev = "TenancyRev".rev + 1
		RETURNING rev`).Scan(&rev)
	if err != nil {
		return
	}
	d.tenancy.bumpLocal(rev)
}

// tenancyRevSnapshot is the per-request entry point: freshens the
// local rev view (≤ one SELECT per poll interval) and returns the rev
// to stamp new cache entries with.
func (d *DB) tenancyRevSnapshot(ctx context.Context) int64 {
	if d.tenancy == nil {
		return 0
	}
	return d.tenancy.revSnapshot(func() (int64, bool) {
		return d.readTenancyRev(ctx)
	})
}

// ListUserTenancyCached returns the user's tenancy from the in-process
// cache when fresh, otherwise from the underlying join. Safe to call
// concurrently. Falls back to the live query if the cache pointer
// isn't initialised yet (test fixtures).
func (d *DB) ListUserTenancyCached(ctx context.Context, userID string) (GroupTenancy, error) {
	if d == nil {
		return GroupTenancy{}, nil
	}
	var rev int64
	if d.tenancy != nil {
		rev = d.tenancyRevSnapshot(ctx)
		if t, ok := d.tenancy.get(userID); ok {
			return t, nil
		}
	}
	t, err := d.ListUserTenancy(ctx, userID)
	if err != nil {
		return GroupTenancy{}, err
	}
	if d.tenancy != nil {
		d.tenancy.put(userID, t, rev)
	}
	return t, nil
}

// UserPermissionsCached returns the user's custom-role permissions
// (UserPermissions) through the same TTL cache tenancy uses. The auth
// middleware's per-request permission resolver calls this on EVERY
// authenticated request — uncached it would re-run the role/permission
// JOIN per request, exactly the load the tenancy cache exists to absorb.
func (d *DB) UserPermissionsCached(ctx context.Context, userID string) ([]string, error) {
	if d == nil {
		return nil, nil
	}
	var rev int64
	if d.tenancy != nil {
		rev = d.tenancyRevSnapshot(ctx)
		if p, ok := d.tenancy.getPerms(userID); ok {
			return p, nil
		}
	}
	p, err := d.UserPermissions(ctx, userID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		p = []string{}
	}
	if d.tenancy != nil {
		d.tenancy.putPerms(userID, p, rev)
	}
	return p, nil
}

// EvictUserTenancy drops the cached entry for one user. Called from
// the same handlers that bump UserTokenInvalidation, so a role/group
// change applied on this replica shows up immediately. Also bumps the
// shared TenancyRev so OTHER replicas converge within the poll
// interval instead of the full cache TTL.
func (d *DB) EvictUserTenancy(userID string) {
	if d == nil || d.tenancy == nil {
		return
	}
	d.tenancy.evict(userID)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.bumpTenancyRev(ctx)
}

// EvictAllTenancy drops every cached entry. Used by group/role bulk
// invalidations (InvalidateUsersByRole, InvalidateUsersByGroup) where
// listing the affected users would itself be a JOIN. Bumps the shared
// TenancyRev for cross-replica convergence (see EvictUserTenancy).
func (d *DB) EvictAllTenancy() {
	if d == nil || d.tenancy == nil {
		return
	}
	d.tenancy.evictAll()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	d.bumpTenancyRev(ctx)
}
