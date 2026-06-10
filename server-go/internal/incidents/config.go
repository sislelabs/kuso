package incidents

import (
	"context"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// configCacheTTL bounds how stale the Manager's view of the config can be.
// 30s matches the build-settings / notify caches: short enough that an admin
// toggling the feature in the UI sees it apply within seconds, long enough
// that a build/crash storm doesn't hammer the Setting table on every event.
const configCacheTTL = 30 * time.Second

// DBConfigProvider reads IncidentAgentConfig from the Setting table with a
// short cache. Implements incidents.ConfigProvider. Invalidate() drops the
// cache so a settings PUT applies immediately (the handler's OnSettingsChange
// hook calls it).
type DBConfigProvider struct {
	DB *db.DB

	mu       sync.RWMutex
	cached   db.IncidentAgentConfig
	cachedAt time.Time
	valid    bool
}

// Get returns the live config, served from cache when fresh. On a DB error
// it returns the last good cache, or defaults (disabled) if never loaded —
// failing closed (no spawns) is the safe direction.
func (p *DBConfigProvider) Get(ctx context.Context) db.IncidentAgentConfig {
	p.mu.RLock()
	if p.valid && time.Since(p.cachedAt) < configCacheTTL {
		c := p.cached
		p.mu.RUnlock()
		return c
	}
	p.mu.RUnlock()

	cfg, err := p.DB.GetIncidentAgentConfig(ctx)
	if err != nil {
		p.mu.RLock()
		defer p.mu.RUnlock()
		if p.valid {
			return p.cached
		}
		return db.DefaultIncidentAgentConfig()
	}
	p.mu.Lock()
	p.cached, p.cachedAt, p.valid = cfg, time.Now(), true
	p.mu.Unlock()
	return cfg
}

// Invalidate drops the cache so the next Get re-reads the DB.
func (p *DBConfigProvider) Invalidate() {
	p.mu.Lock()
	p.valid = false
	p.mu.Unlock()
}
