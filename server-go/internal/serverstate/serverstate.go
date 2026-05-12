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
)

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
