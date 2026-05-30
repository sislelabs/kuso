// Package serverstate holds process-wide flags that affect readiness
// and request gating but aren't owned by any single subsystem.
//
// CRD-stale is the canonical example: when the schema preflight at
// boot detects that a kuso CRD on the live cluster is missing a
// field this build expects, we want readyz to fail with a precise
// message AND every /api/* write to refuse — but the server still
// needs to serve the SPA + /api/auth/session so an operator can
// log in and see the banner. A package-level state pointer lets
// main.go set it during boot and the router middleware read it on
// every request, without threading a flag through the Deps struct
// or the chi.Router constructor.
package serverstate

import (
	"sync"
	"time"
)

// CRDStaleInfo is the set of expected-but-missing field paths the
// schema preflight surfaced. Empty slice = CRDs are healthy. Non-
// empty = block writes, fail readyz, log loudly.
type CRDStaleInfo struct {
	Mismatches []string
}

var (
	mu       sync.RWMutex
	crdStale *CRDStaleInfo

	// leading is true while THIS pod holds the leader lease (and thus
	// runs the singleton workers — build poller, etc.). readyz only
	// enforces the poller heartbeat when leading, so a non-leader pod
	// (which never runs the poller) doesn't fail readiness.
	leading bool
	// pollerBeat is the time of the build poller's last completed tick.
	// readyz treats a stale beat (while leading) as "the poller
	// goroutine died silently" → fail readyz → the LB drains + the pod
	// is eventually restarted, releasing leadership to a healthy pod.
	pollerBeat time.Time
)

// SetLeading records whether this pod currently holds leadership. Wired
// from the leader-election OnStartedLeading / OnStoppedLeading hooks.
func SetLeading(v bool) {
	mu.Lock()
	leading = v
	if !v {
		// Reset the beat on losing leadership so a stale timestamp from
		// a previous tenure can't make a re-elected pod look unhealthy
		// before its poller has ticked again.
		pollerBeat = time.Time{}
	}
	mu.Unlock()
}

// PollerHeartbeat stamps the current time as the build poller's last
// successful tick. Called from the poller loop.
func PollerHeartbeat() {
	mu.Lock()
	pollerBeat = time.Now()
	mu.Unlock()
}

// PollerHealthy reports whether the build poller looks alive. Returns
// (healthy, leading). When not leading, healthy is always true (this pod
// doesn't run the poller). When leading, it's healthy iff the last beat
// is within maxStale — with a grace window after becoming leader before
// the first beat lands (caller passes the grace via firstBeatBy).
func PollerHealthy(maxStale time.Duration) (healthy, isLeading bool) {
	mu.RLock()
	defer mu.RUnlock()
	if !leading {
		return true, false
	}
	// Leading but the poller has never beat yet: treat as healthy
	// (grace) — the poller starts a moment after leadership and the
	// first tick is ~5s out. A genuinely dead poller is caught once a
	// beat lands and then goes stale, or never lands and the next check
	// after maxStale-from-leadership flags it. We keep it simple: a
	// zero beat is "warming up", healthy.
	if pollerBeat.IsZero() {
		return true, true
	}
	return time.Since(pollerBeat) <= maxStale, true
}

// SetCRDStale records the preflight result. Call once at boot.
// nil clears (the server became healthy; not used today but a
// future runtime re-check could).
func SetCRDStale(info *CRDStaleInfo) {
	mu.Lock()
	crdStale = info
	mu.Unlock()
}

// CRDStale returns the current preflight state. The returned
// pointer is read-only; callers should not mutate Mismatches.
func CRDStale() *CRDStaleInfo {
	mu.RLock()
	defer mu.RUnlock()
	return crdStale
}
