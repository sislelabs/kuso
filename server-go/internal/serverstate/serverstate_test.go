package serverstate

import (
	"testing"
	"time"
)

// TestPollerStuckFor covers the leader-gated heartbeat staleness clock:
// non-leader always (0,false); leader with a beat measures from the
// beat; leader that NEVER beat measures from leadership start (the
// "never beat = healthy forever" hole this replaced PollerHealthy to
// close); losing leadership resets everything.
func TestPollerStuckFor(t *testing.T) {
	// Not parallel — mutates package state.
	t.Cleanup(func() { SetLeading(false) })

	// Non-leader: never stuck, leading=false.
	SetLeading(false)
	if s, l := PollerStuckFor(); s != 0 || l {
		t.Errorf("non-leader: got stuck=%v leading=%v, want 0,false", s, l)
	}

	// Become leader, no beat yet → stuck is measured from leadSince,
	// which is ~now, so it must be tiny (startup grace comes from the
	// caller's threshold, not from a zero reading).
	SetLeading(true)
	if s, l := PollerStuckFor(); s > 5*time.Second || !l {
		t.Errorf("fresh leader no beat: got stuck=%v leading=%v, want ~0,true", s, l)
	}

	// NEVER-beat hole: a leader whose poller never stamped a beat must
	// eventually read as stuck. Simulate 10 minutes of leadership with
	// no beat by back-dating leadSince.
	mu.Lock()
	leadSince = time.Now().Add(-10 * time.Minute)
	pollerBeat = time.Time{}
	mu.Unlock()
	if s, l := PollerStuckFor(); s < 9*time.Minute || !l {
		t.Errorf("never-beat leader: got stuck=%v leading=%v, want >=9m,true", s, l)
	}

	// Fresh beat → stuck resets to ~0.
	PollerHeartbeat()
	if s, l := PollerStuckFor(); s > 5*time.Second || !l {
		t.Errorf("fresh beat: got stuck=%v leading=%v, want ~0,true", s, l)
	}

	// Stale beat → stuck measured from the beat.
	mu.Lock()
	pollerBeat = time.Now().Add(-time.Hour)
	mu.Unlock()
	if s, l := PollerStuckFor(); s < 59*time.Minute || !l {
		t.Errorf("stale beat: got stuck=%v leading=%v, want >=59m,true", s, l)
	}

	// Losing leadership resets the beat AND the leadSince anchor.
	SetLeading(false)
	mu.RLock()
	beatReset, sinceReset := pollerBeat.IsZero(), leadSince.IsZero()
	mu.RUnlock()
	if !beatReset || !sinceReset {
		t.Errorf("losing leadership must reset beat+leadSince, got beatZero=%v sinceZero=%v", beatReset, sinceReset)
	}
	if s, l := PollerStuckFor(); s != 0 || l {
		t.Errorf("after losing leadership: got stuck=%v leading=%v, want 0,false", s, l)
	}

	// Re-election after a stale tenure must not inherit the old clock:
	// leadSince re-anchors to now.
	SetLeading(true)
	if s, l := PollerStuckFor(); s > 5*time.Second || !l {
		t.Errorf("re-elected leader: got stuck=%v leading=%v, want ~0,true", s, l)
	}
}

// TestUnregisteredLoopIsInvisible is the Finding-1 regression guard: a
// self-disabling loop that early-returns without ever stamping a heartbeat
// (scaledown under KUSO_SCALEDOWN_DISABLED) must NOT be enrolled, because a
// registered-but-never-beating loop is measured from registeredAt forever
// and would trip /healthz within minutes. The fix registers such a loop
// only when it will run+beat, so the "disabled" path leaves the registry
// with no entry for it — LoopStaleness never reports it, probes never gate.
func TestUnregisteredLoopIsInvisible(t *testing.T) {
	t.Cleanup(UnregisterLoops)
	UnregisterLoops()

	// Simulate the DISABLED path: the loop's RegisterLoop is guarded and
	// therefore NOT called. Register a *different* loop so the registry is
	// non-empty (mirrors a real leader with other healthy loops running).
	RegisterLoop(LoopAlerts, time.Minute)
	LoopHeartbeat(LoopAlerts) // it beats normally

	// The disabled loop stamps no heartbeat (its Run returned early). Even
	// if a stray beat were emitted, an unregistered name is a no-op.
	LoopHeartbeat(LoopScaledown)

	stale := LoopStaleness()
	for _, ls := range stale {
		if ls.Name == LoopScaledown {
			t.Fatalf("disabled scaledown must not appear in the registry; got %+v", stale)
		}
	}

	// And even after a long simulated silence, no entry for it exists to
	// go stale — so a /healthz built off LoopStaleness can never 503 on it.
	// (Contrast: a registered-but-never-beating loop WOULD go stale, proven
	// by TestRegisteredNeverBeatGoesStale below.)
	if len(stale) != 1 || stale[0].Name != LoopAlerts {
		t.Fatalf("expected only the healthy alerts loop registered, got %+v", stale)
	}
}

// TestRegisteredNeverBeatGoesStale proves the flip side: a loop that IS
// registered but never beats is measured from registration and does go
// stale — which is exactly why an env-disabled, never-beating loop must
// NOT be registered (Finding 1). This is the failure mode the guard avoids.
func TestRegisteredNeverBeatGoesStale(t *testing.T) {
	t.Cleanup(UnregisterLoops)
	UnregisterLoops()

	RegisterLoop(LoopScaledown, time.Minute)
	// Never call LoopHeartbeat. Backdate registration to simulate elapsed time.
	BackdateLoopForTest(LoopScaledown, 30*time.Minute)

	stale := LoopStaleness()
	if len(stale) != 1 {
		t.Fatalf("want 1 registered loop, got %+v", stale)
	}
	if stale[0].StaleFor < 29*time.Minute {
		t.Fatalf("registered-never-beat loop must read stale from registration; got StaleFor=%v", stale[0].StaleFor)
	}
}

// TestUnregisterLoopTargeted guards the two-lease fix: UnregisterLoop drops
// only the named loops, leaving a sibling lease's loops intact. A blanket
// UnregisterLoops() from one lease's cancel path must not be what we use, or
// it would strand the other lease's still-running loops.
func TestUnregisterLoopTargeted(t *testing.T) {
	t.Cleanup(UnregisterLoops)
	UnregisterLoops()

	// Two loops as if owned by two different leases.
	RegisterLoop(LoopAlerts, time.Minute)       // cluster-singletons lease
	RegisterLoop(LoopRunsPoller, 5*time.Second) // kuso-server-singletons lease

	// The kuso-server-singletons lease cancels and drops only its own.
	UnregisterLoop(LoopRunsPoller)

	stale := LoopStaleness()
	if len(stale) != 1 || stale[0].Name != LoopAlerts {
		t.Fatalf("targeted unregister must leave the sibling lease's loop; got %+v", stale)
	}

	// Unknown / already-dropped names are ignored (no panic, no-op).
	UnregisterLoop(LoopRunsPoller, "does-not-exist")
	if s := LoopStaleness(); len(s) != 1 {
		t.Fatalf("unregistering unknown names must be a no-op; got %+v", s)
	}
}
