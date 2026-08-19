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
	// leadSince is when this pod acquired leadership. It anchors the
	// "never beat yet" staleness clock: a leader whose poller has NEVER
	// stamped a beat is measured from leadership start, so a poller that
	// wedges before its first tick is still eventually detected instead
	// of counting as healthy forever.
	leadSince time.Time
	// pollerBeat is the time of the build poller's last completed tick.
	// A stale beat (while leading) means "the poller goroutine died
	// silently": readyz fails first (LB drains), and past a much longer
	// hard-stuck window healthz fails too, so the kubelet restarts the
	// pod and the lease is released to a healthy replica. Without the
	// liveness half, a wedged replicas=1 leader blackholed the API while
	// holding the lease forever — readiness alone never restarts a pod.
	pollerBeat time.Time

	// loops is the heartbeat registry for the OTHER singleton control-
	// plane loops (nodewatch, alerts, remediate, incidents, scaledown,
	// imagerelease, cronwatch, pkgupdates, backuphealth, logship, the two
	// metric samplers — everything under the "cluster-singletons" lease —
	// plus the kuso-server-singletons-lease loops runs-poller, errorscan,
	// health and instancepg, plus anything else that opts in). The build poller predates
	// this and keeps its own pollerBeat/leading fields for compatibility
	// with the existing, well-tested probe path; the registry is additive.
	//
	// A loop is present in this map ONLY while the pod that runs it is
	// actively holding the relevant lease: the lease-holder closure calls
	// RegisterLoop when it starts each loop and UnregisterLoops when the
	// lease context is cancelled. That makes the registry inherently
	// leader-aware — a non-leader pod registers nothing, so LoopStaleness
	// returns an empty slice and the probes never gate on it. The exact
	// mirror of how `leading` gates the poller path.
	loops = map[string]*loopState{}
)

// Loop names for the singleton-loop heartbeat registry. Registration
// (main.go, at the lease-holder closure) and per-iteration heartbeats (the
// loop bodies) both reference these so the two sides can't drift on a
// stringly-typed key. Only loops that stamp a heartbeat are listed; loops
// deliberately EXCLUDED from liveness (see main.go's startKubeSingletons)
// have no constant here.
const (
	LoopNodeMetrics    = "nodemetrics"
	LoopProjectMetrics = "projectmetrics"
	LoopNodeWatch      = "nodewatch"
	LoopAlerts         = "alerts"
	LoopScaledown      = "scaledown"
	LoopImageRelease   = "imagerelease"
	LoopCronWatch      = "cronwatch"
	LoopPkgUpdates     = "pkgupdates"
	LoopBackupHealth   = "backuphealth"
	LoopIncidents      = "incidents"
	LoopAutoRemediate  = "auto-remediate"
	LoopLogship        = "logship"

	// Loops under the SECOND lease (kuso-server-singletons, startSingletons
	// in main.go). Same registry, same leader-gated discipline — registered
	// only when they'll actually run+beat. Ticking loops only; the once-and-
	// return platformharden pass is deliberately EXCLUDED (it can't stamp a
	// fixed-interval heartbeat) — see startSingletons.
	LoopRunsPoller = "runs-poller"
	LoopErrorScan  = "errorscan"
	LoopHealth     = "health"
	LoopInstancePG = "instancepg"
)

// loopState tracks one registered singleton loop's liveness signal.
type loopState struct {
	// interval is the loop's expected tick cadence. Per-loop probe
	// thresholds are derived from THIS, not a global constant, so a
	// 5-min sampler and a 15-s watcher get proportionate windows and a
	// slow loop is never falsely flagged at a fast loop's threshold.
	interval time.Duration
	// registeredAt anchors the "never beat yet" clock — the per-loop
	// equivalent of leadSince. A loop that wedges before its first tick
	// is measured from registration, so it still trips the thresholds
	// instead of counting as healthy forever.
	registeredAt time.Time
	// lastBeat is the loop's last completed-iteration heartbeat; zero
	// until the first beat, in which case staleness is measured from
	// registeredAt.
	lastBeat time.Time
}

// LoopStale is one registered loop's staleness snapshot, consumed by the
// probe handlers which own the readiness/liveness thresholds (they differ
// by an order of magnitude and a false liveness failure is itself an
// outage — see internal/http/probes.go).
type LoopStale struct {
	Name string
	// StaleFor is how long since the loop's last heartbeat (or since
	// registration if it never beat).
	StaleFor time.Duration
	// Interval is the loop's expected tick cadence, so the probe can
	// derive per-loop thresholds.
	Interval time.Duration
}

// RegisterLoop enrolls a singleton loop in the heartbeat registry with
// its expected tick interval. Call from the lease-holder closure as each
// loop starts. Idempotent per name: re-registering (e.g. a re-elected
// leader restarting its loops) re-anchors the never-beat clock and clears
// any stale beat from a previous tenure, so the loop isn't born "stuck".
func RegisterLoop(name string, interval time.Duration) {
	mu.Lock()
	loops[name] = &loopState{interval: interval, registeredAt: time.Now()}
	mu.Unlock()
}

// UnregisterLoops drops every registered loop. Call when the lease context
// is cancelled (leadership lost / shutdown) so a stale beat from a former
// tenure can't make a non-leader — or a re-elected pod before its loops
// restart — read as unhealthy. Mirrors SetLeading(false) resetting the
// poller clock.
//
// NOTE: this wipes the WHOLE map. Only safe to call from a context that
// owns every registered loop. Two independent leases now register into
// this registry (cluster-singletons and kuso-server-singletons); each
// must drop only ITS OWN loops on lease loss via UnregisterLoop(names…),
// NOT this blanket clear, or one lease's cancel would strand the other's
// still-running loops (their heartbeats would keep landing on a wiped
// entry — a no-op — so those loops would read as unregistered/ignored).
func UnregisterLoops() {
	mu.Lock()
	loops = map[string]*loopState{}
	mu.Unlock()
}

// UnregisterLoop drops only the named loops. Call from a lease-holder
// closure's cancel path to unregister exactly the loops that closure
// started, leaving loops owned by a sibling lease untouched. Unknown
// names are ignored.
func UnregisterLoop(names ...string) {
	mu.Lock()
	for _, n := range names {
		delete(loops, n)
	}
	mu.Unlock()
}

// LoopHeartbeat stamps the current time as the named loop's last completed
// tick. Call once per loop iteration (even an iteration that logged an
// error — the loop is alive, which is what liveness cares about). A no-op
// for an unregistered name, so a loop whose lease was just lost mid-tick
// doesn't resurrect a registry entry.
func LoopHeartbeat(name string) {
	mu.Lock()
	if ls, ok := loops[name]; ok {
		ls.lastBeat = time.Now()
	}
	mu.Unlock()
}

// LoopStaleness returns a staleness snapshot for every registered loop.
// Empty when this pod holds no lease (nothing registered) — so probes on
// a non-leader never gate on loop health. For each loop, staleness is
// measured from its last beat, or from registration if it never beat (the
// per-loop "never beat = healthy forever" hole is closed exactly as the
// poller's leadSince closes it).
func LoopStaleness() []LoopStale {
	mu.RLock()
	defer mu.RUnlock()
	if len(loops) == 0 {
		return nil
	}
	now := time.Now()
	out := make([]LoopStale, 0, len(loops))
	for name, ls := range loops {
		anchor := ls.lastBeat
		if anchor.IsZero() {
			anchor = ls.registeredAt
		}
		out = append(out, LoopStale{
			Name:     name,
			StaleFor: now.Sub(anchor),
			Interval: ls.interval,
		})
	}
	return out
}

// BackdateLoopForTest shifts a registered loop's clocks into the past so
// probe tests can simulate a wedged loop without sleeping wall time. It
// backdates BOTH registeredAt and (if set) lastBeat, so the effect holds
// whether or not the loop has beaten. TEST HOOK ONLY. No-op for an
// unregistered name.
func BackdateLoopForTest(name string, d time.Duration) {
	mu.Lock()
	if ls, ok := loops[name]; ok {
		ls.registeredAt = ls.registeredAt.Add(-d)
		if !ls.lastBeat.IsZero() {
			ls.lastBeat = ls.lastBeat.Add(-d)
		}
	}
	mu.Unlock()
}

// SetLeading records whether this pod currently holds leadership. Wired
// from the leader-election OnStartedLeading / OnStoppedLeading hooks.
func SetLeading(v bool) {
	mu.Lock()
	leading = v
	if v {
		// Anchor the never-beat staleness clock to this tenure's start.
		leadSince = time.Now()
	} else {
		// Reset the beat on losing leadership so a stale timestamp from
		// a previous tenure can't make a re-elected pod look unhealthy
		// before its poller has ticked again.
		pollerBeat = time.Time{}
		leadSince = time.Time{}
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

// PollerStuckFor reports how long the leader's build poller has gone
// without a heartbeat, and whether this pod is leading. When not
// leading it returns (0, false) — this pod doesn't run the poller.
// When leading:
//   - beat recorded → time since the last beat.
//   - never beat yet → time since leadership was acquired. This closes
//     the "never beat = healthy forever" hole: a poller that wedges
//     before its first tick is measured from leadership start, so it
//     still trips the readiness (and eventually liveness) thresholds.
//     The first tick lands ~5s after acquiring the lease, so any
//     threshold ≥ a few ticks doubles as the startup grace window.
//
// This is deliberately a staleness DURATION, not a healthy/unhealthy
// verdict: readiness and liveness apply very different thresholds to
// the same clock (readyz drains fast; healthz restarts only on a long
// hard-stuck window, because a false liveness failure is itself an
// outage). The probe handlers own the thresholds — see
// internal/http/probes.go.
func PollerStuckFor() (stuck time.Duration, isLeading bool) {
	mu.RLock()
	defer mu.RUnlock()
	if !leading {
		return 0, false
	}
	if pollerBeat.IsZero() {
		return time.Since(leadSince), true
	}
	return time.Since(pollerBeat), true
}

// BackdateLeadershipForTest shifts the leadership-start anchor into the
// past. TEST HOOK ONLY — lets probe tests simulate a leader whose poller
// never beat for `d` without sleeping wall time. No-op when not leading.
func BackdateLeadershipForTest(d time.Duration) {
	mu.Lock()
	if leading {
		leadSince = leadSince.Add(-d)
	}
	mu.Unlock()
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
