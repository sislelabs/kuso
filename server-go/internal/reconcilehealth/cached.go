package reconcilehealth

import (
	"context"
	"sync"
	"time"
)

// CachedScanner serves the report endpoint. Every open Settings → Health
// tab polls every 30s, and each poll used to run a full scan. Scans are
// serialised, so concurrent polls wait for one scan instead of each
// starting their own. Failed scans are not cached.
//
// Only the read-only report goes through this. Remediation re-scans with
// the plain Scanner so it always acts on current state.
type CachedScanner struct {
	Scanner *Scanner
	TTL     time.Duration
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	ns      string
	rep     *Report
	scanned time.Time
}

// Scan returns the last report for namespace if it is younger than TTL,
// else runs a fresh scan. The returned Report is shared; callers must not
// mutate it.
func (c *CachedScanner) Scan(ctx context.Context, namespace string) (*Report, error) {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.rep != nil && c.ns == namespace && now().Sub(c.scanned) < c.TTL {
		return c.rep, nil
	}
	rep, err := c.Scanner.Scan(ctx, namespace)
	if err != nil {
		return nil, err
	}
	c.ns, c.rep, c.scanned = namespace, rep, now()
	return rep, nil
}
