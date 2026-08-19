package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/serverstate"
)

// stubPollerClock replaces the poller staleness clock for the duration
// of a test. Not parallel-safe (package-level state) — callers must not
// use t.Parallel().
func stubPollerClock(t *testing.T, stuck time.Duration, leading bool) {
	t.Helper()
	prev := pollerStuckFor
	pollerStuckFor = func() (time.Duration, bool) { return stuck, leading }
	t.Cleanup(func() { pollerStuckFor = prev })
}

func doHealthz(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	healthz(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	return rec
}

func doReadyz(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	readyz(Deps{})(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	return rec
}

// TestProbes_StaleWithinLivenessWindow: a leader whose poller beat is
// stale past the readiness window but inside the liveness window must be
// LIVE (no restart — could be a slow tick) but UNREADY (LB drains it).
func TestProbes_StaleWithinLivenessWindow(t *testing.T) {
	stubPollerClock(t, readyzPollerMaxStale+30*time.Second, true) // 60s stuck

	if rec := doHealthz(t); rec.Code != http.StatusOK {
		t.Errorf("healthz: got %d, want 200 — a 60s-stale beat must never restart the pod", rec.Code)
	}
	rec := doReadyz(t)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz: got %d, want 503 (LB must drain a stalled leader)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"buildPoller":"stalled"`) {
		t.Errorf("readyz body missing stalled check: %s", rec.Body.String())
	}
}

// TestProbes_HardStuck: past the hard-stuck window the pod must fail
// LIVENESS so the kubelet restarts it and the leader lease is released —
// readiness alone never restarts a pod, which is exactly how a wedged
// replicas=1 leader used to blackhole the API forever.
func TestProbes_HardStuck(t *testing.T) {
	stubPollerClock(t, healthzPollerHardStuck+time.Minute, true)

	rec := doHealthz(t)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz: got %d, want 503 for a hard-stuck leader", rec.Code)
	}
	// The version field must survive on the failure body — release
	// tooling greps it.
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if body["status"] != "stuck" || body["version"] == "" {
		t.Errorf("healthz 503 body = %v, want status=stuck + non-empty version", body)
	}
	if rec := doReadyz(t); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz: got %d, want 503", rec.Code)
	}
}

// TestProbes_NeverBeatPastGrace: a leader whose poller NEVER stamped a
// beat is measured from leadership start (serverstate.PollerStuckFor),
// so past the hard-stuck window it must fail liveness too — the old
// "zero beat = healthy forever" hole. Exercised through the real
// serverstate clock, not the stub, to cover the whole path.
func TestProbes_NeverBeatPastGrace(t *testing.T) {
	t.Cleanup(func() { serverstate.SetLeading(false) })
	serverstate.SetLeading(true)
	serverstate.BackdateLeadershipForTest(healthzPollerHardStuck + time.Minute)

	if rec := doHealthz(t); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("healthz: got %d, want 503 — never-beaten leader past grace must not be live", rec.Code)
	}
}

// TestProbes_NonLeaderAlwaysLive: a non-leader never runs the poller, so
// no amount of "staleness" may affect it.
func TestProbes_NonLeaderAlwaysLive(t *testing.T) {
	stubPollerClock(t, 0, false)
	if rec := doHealthz(t); rec.Code != http.StatusOK {
		t.Errorf("healthz non-leader: got %d, want 200", rec.Code)
	}
	rec := doReadyz(t)
	if rec.Code != http.StatusOK {
		t.Errorf("readyz non-leader: got %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "buildPoller") {
		t.Errorf("readyz non-leader must not report a buildPoller check: %s", rec.Body.String())
	}
}

// TestProbes_DegradedModeStaysLive: CRD-stale degraded mode is a
// deliberate flagged state — readyz goes unready (so the banner and the
// operator's curl see it) but healthz MUST stay live: restarting the pod
// can't fix stale CRDs and would just crash-loop the operator out of the
// UI they need to see the banner in.
func TestProbes_DegradedModeStaysLive(t *testing.T) {
	stubPollerClock(t, 0, true) // healthy poller, leading
	serverstate.SetCRDStale(&serverstate.CRDStaleInfo{Mismatches: []string{"kusoservices: spec.newField"}})
	t.Cleanup(func() { serverstate.SetCRDStale(nil) })

	if rec := doHealthz(t); rec.Code != http.StatusOK {
		t.Errorf("healthz degraded: got %d, want 200 (degraded mode must stay live)", rec.Code)
	}
	rec := doReadyz(t)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz degraded: got %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "crd") {
		t.Errorf("readyz degraded body missing crd check: %s", rec.Body.String())
	}
}

// stubLoopStaleness replaces the singleton-loop staleness source for the
// duration of a test. Not parallel-safe.
func stubLoopStaleness(t *testing.T, out []serverstate.LoopStale) {
	t.Helper()
	prev := loopStaleness
	loopStaleness = func() []serverstate.LoopStale { return out }
	t.Cleanup(func() { loopStaleness = prev })
}

// TestProbes_DisabledLoopNeverTripsHealthz is the Finding-1 regression
// guard at the probe layer: a loop that self-disables (scaledown under
// KUSO_SCALEDOWN_DISABLED) and never beats is NOT registered, so it never
// appears in LoopStaleness — and /healthz stays 200 no matter how much
// wall time passes. The bug was that the loop WAS registered while its
// Run early-returned, so it aged from registeredAt and 503'd ~10min after
// every leader election, restart-looping the pod. Here we model the fixed
// state: the disabled loop is absent, only genuinely-beating loops present.
func TestProbes_DisabledLoopNeverTripsHealthz(t *testing.T) {
	stubPollerClock(t, 0, true) // healthy poller, leading
	// Registry as it looks with scaledown DISABLED (thus unregistered):
	// only loops that actually beat are present, all fresh.
	stubLoopStaleness(t, []serverstate.LoopStale{
		{Name: "alerts", StaleFor: time.Second, Interval: time.Minute},
		{Name: "nodewatch", StaleFor: 2 * time.Second, Interval: 15 * time.Second},
	})
	if rec := doHealthz(t); rec.Code != http.StatusOK {
		t.Fatalf("healthz with a disabled (unregistered) scaledown: got %d, want 200 — an env-disabled loop must never gate liveness", rec.Code)
	}
	if rec := doReadyz(t); rec.Code != http.StatusOK {
		t.Fatalf("readyz with a disabled (unregistered) scaledown: got %d, want 200", rec.Code)
	}
}

// TestProbes_RegisteredLoopHardStuck503s proves the mechanism the guard
// protects against: had the disabled loop stayed registered, it would age
// past its per-interval hard window and 503 liveness. This is why the
// registration must be gated on "will run+beat."
func TestProbes_RegisteredLoopHardStuck503s(t *testing.T) {
	stubPollerClock(t, 0, true)
	stubLoopStaleness(t, []serverstate.LoopStale{
		// A 1-min-interval loop stale far past healthzLoopHardStuck(1m).
		{Name: "scaledown", StaleFor: 30 * time.Minute, Interval: time.Minute},
	})
	if rec := doHealthz(t); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("healthz with a registered hard-stuck loop: got %d, want 503", rec.Code)
	}
}
