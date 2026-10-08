package uptime

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// run feeds one outcome per minute and returns the actions in order.
func run(t *testing.T, s State, steps []step) (State, []Action) {
	t.Helper()
	var acts []Action
	for i, st := range steps {
		var d Decision
		s, d = Decide(s, st.o, Signals{PodsBad: st.podsBad, RollingOut: st.rolling}, t0.Add(time.Duration(i)*time.Minute))
		acts = append(acts, d.Action)
	}
	return s, acts
}

type step struct {
	o       Outcome
	podsBad bool
	rolling bool
}

func rep(o Outcome, n int) []step {
	out := make([]step, n)
	for i := range out {
		out[i] = step{o: o}
	}
	return out
}

func count(acts []Action, a Action) int {
	n := 0
	for _, x := range acts {
		if x == a {
			n++
		}
	}
	return n
}

func TestBlipNeverAlerts(t *testing.T) {
	steps := append(rep(OutcomeFail, 2), rep(OutcomeOK, 5)...)
	s, acts := run(t, State{}, steps)
	if count(acts, ActionDown)+count(acts, ActionRecovered) != 0 {
		t.Fatalf("blip produced actions: %v", acts)
	}
	if !s.DownSince.IsZero() {
		t.Fatalf("outage still open after 5 good checks: %+v", s)
	}
}

func TestIntermittentNeverAlerts(t *testing.T) {
	var steps []step
	for i := 0; i < 10; i++ {
		steps = append(steps, step{o: OutcomeFail}, step{o: OutcomeFail}, step{o: OutcomeOK})
	}
	_, acts := run(t, State{}, steps)
	if count(acts, ActionDown) != 0 {
		t.Fatalf("fail,fail,ok pattern alerted: %v", acts)
	}
}

func TestThreeFailuresAlertOnceThenRecoverOnce(t *testing.T) {
	steps := append(rep(OutcomeFail, 10), rep(OutcomeOK, 6)...)
	s, acts := run(t, State{}, steps)
	if acts[0] != ActionNone || acts[1] != ActionNone || acts[2] != ActionDown {
		t.Fatalf("want down on the 3rd failure, got %v", acts[:3])
	}
	if count(acts, ActionDown) != 1 {
		t.Fatalf("want exactly one down, got %v", acts)
	}
	// Recovery needs 3 good checks: steps 10,11 are quiet, 12 recovers.
	if acts[10] != ActionNone || acts[11] != ActionNone || acts[12] != ActionRecovered {
		t.Fatalf("want recovered on the 3rd good check, got %v", acts[10:13])
	}
	if count(acts, ActionRecovered) != 1 {
		t.Fatalf("want exactly one recovered, got %v", acts)
	}
	if s.Alerted || !s.DownSince.IsZero() {
		t.Fatalf("outage not closed: %+v", s)
	}
	if !s.LastAlertAt.Equal(t0.Add(2 * time.Minute)) {
		t.Fatalf("lastAlertAt = %v", s.LastAlertAt)
	}
}

func TestRecoveredReportsDowntime(t *testing.T) {
	s := State{}
	var d Decision
	for i := 0; i < 5; i++ { // fails at minutes 0..4
		s, _ = Decide(s, OutcomeFail, Signals{}, t0.Add(time.Duration(i)*time.Minute))
	}
	for i := 5; i < 8; i++ { // ok at minutes 5..7
		s, d = Decide(s, OutcomeOK, Signals{}, t0.Add(time.Duration(i)*time.Minute))
	}
	if d.Action != ActionRecovered {
		t.Fatalf("want recovered, got %v", d.Action)
	}
	// First failure at minute 0, first good check at minute 5.
	if d.DownFor != 5*time.Minute {
		t.Fatalf("DownFor = %v, want 5m", d.DownFor)
	}
}

func TestFlapInsideCooldownIsSilentThenAlertsWhenStillDown(t *testing.T) {
	// Down at min 2, recovered at min 7.
	steps := append(rep(OutcomeFail, 5), rep(OutcomeOK, 3)...)
	// Fails again from min 8 and stays down past the 30-min cooldown
	// (last alert at min 2 → cooldown ends at min 32).
	steps = append(steps, rep(OutcomeFail, 30)...)
	_, acts := run(t, State{}, steps)
	if acts[2] != ActionDown || acts[7] != ActionRecovered {
		t.Fatalf("first outage: %v", acts[:8])
	}
	for i := 8; i < 32; i++ {
		if acts[i] != ActionNone {
			t.Fatalf("minute %d inside cooldown produced %v", i, acts[i])
		}
	}
	if acts[32] != ActionDown {
		t.Fatalf("want down at minute 32 when the cooldown ends, got %v", acts[32])
	}
	if count(acts, ActionDown) != 2 {
		t.Fatalf("want 2 downs in total, got %v", acts)
	}
}

func TestSilentOutageClosesSilently(t *testing.T) {
	steps := append(rep(OutcomeFail, 5), rep(OutcomeOK, 3)...) // down@2, recovered@7
	steps = append(steps, rep(OutcomeFail, 5)...)              // silent (cooldown)
	steps = append(steps, rep(OutcomeOK, 5)...)                // closes silently
	s, acts := run(t, State{}, steps)
	if count(acts, ActionDown) != 1 || count(acts, ActionRecovered) != 1 {
		t.Fatalf("want 1 down + 1 recovered, got %v", acts)
	}
	if !s.DownSince.IsZero() {
		t.Fatalf("silent outage still open: %+v", s)
	}
}

func TestCrashLoopSuppressedThenAlertsWhenPodsFine(t *testing.T) {
	steps := []step{}
	for i := 0; i < 6; i++ {
		steps = append(steps, step{o: OutcomeFail, podsBad: true})
	}
	steps = append(steps, step{o: OutcomeFail}) // crash-loop gone, still failing
	s, acts := run(t, State{}, steps)
	for i := 0; i < 6; i++ {
		if acts[i] != ActionNone {
			t.Fatalf("crash-looping minute %d produced %v", i, acts[i])
		}
	}
	if acts[6] != ActionDown {
		t.Fatalf("want down once pods are fine but checks still fail, got %v", acts[6])
	}
	if !s.Alerted {
		t.Fatal("state not marked alerted")
	}
}

func TestCrashLoopOutageRecoversSilently(t *testing.T) {
	var steps []step
	for i := 0; i < 6; i++ {
		steps = append(steps, step{o: OutcomeFail, podsBad: true})
	}
	steps = append(steps, rep(OutcomeOK, 4)...)
	_, acts := run(t, State{}, steps)
	if count(acts, ActionDown)+count(acts, ActionRecovered) != 0 {
		t.Fatalf("suppressed outage sent something: %v", acts)
	}
}

func TestRestartWithAlertedOutageSendsNothingNew(t *testing.T) {
	// State as loaded from the DB after a restart, mid-outage.
	s := State{FailStreak: 7, DownSince: t0.Add(-7 * time.Minute), Alerted: true, LastAlertAt: t0.Add(-5 * time.Minute)}
	_, acts := run(t, s, rep(OutcomeFail, 40))
	if count(acts, ActionDown) != 0 {
		t.Fatalf("already-alerted outage re-alerted: %v", acts)
	}
}

func TestPausedClosesUnalertedOutageQuietly(t *testing.T) {
	s, acts := run(t, State{}, append(rep(OutcomeFail, 2), step{o: OutcomePaused}))
	if acts[2] != ActionNone {
		t.Fatalf("pausing a confirming outage sent %v", acts[2])
	}
	if !s.DownSince.IsZero() || s.FailStreak != 0 {
		t.Fatalf("pause did not close the outage: %+v", s)
	}
}

func TestPausedClosesAlertedOutageWithMessageAndKeepsCooldown(t *testing.T) {
	s, acts := run(t, State{}, append(rep(OutcomeFail, 4), step{o: OutcomePaused}))
	if acts[4] != ActionClosed {
		t.Fatalf("pausing an alerted outage sent %v, want closed", acts[4])
	}
	if s.Alerted || !s.DownSince.IsZero() || s.FailStreak != 0 {
		t.Fatalf("pause did not close the outage: %+v", s)
	}
	if s.LastAlertAt.IsZero() {
		t.Fatal("pause dropped lastAlertAt")
	}
}

func TestHoldReasons(t *testing.T) {
	s, _ := run(t, State{}, rep(OutcomeFail, 2))
	if s.Hold != HoldConfirming {
		t.Fatalf("hold = %q, want confirming", s.Hold)
	}
	s, _ = run(t, State{}, []step{{o: OutcomeFail, podsBad: true}, {o: OutcomeFail, podsBad: true}, {o: OutcomeFail, podsBad: true}})
	if s.Hold != HoldCrashLoop {
		t.Fatalf("hold = %q, want crash-looping", s.Hold)
	}
	s, _ = run(t, State{LastAlertAt: t0.Add(-time.Minute)}, rep(OutcomeFail, 3))
	if s.Hold != HoldCooldown {
		t.Fatalf("hold = %q, want cooldown", s.Hold)
	}
	s, _ = run(t, State{}, rep(OutcomeFail, 3))
	if s.Hold != "" || !s.Alerted {
		t.Fatalf("alerted outage has hold %q", s.Hold)
	}
}

// A surge pod stuck in CrashLoopBackOff must not silence a later outage
// of the same env forever.
func TestCrashLoopHoldIsCapped(t *testing.T) {
	var steps []step
	for i := 0; i < 30; i++ {
		steps = append(steps, step{o: OutcomeFail, podsBad: true})
	}
	_, acts := run(t, State{}, steps)
	at := int(MaxHold / time.Minute)
	for i := 0; i < at; i++ {
		if acts[i] != ActionNone {
			t.Fatalf("minute %d inside the hold produced %v", i, acts[i])
		}
	}
	if acts[at] != ActionDown {
		t.Fatalf("want down once the hold cap passes (minute %d), got %v", at, acts[at])
	}
}

func TestRolloutHoldsThenAlertsWhenItOverruns(t *testing.T) {
	var steps []step
	for i := 0; i < 5; i++ {
		steps = append(steps, step{o: OutcomeFail, rolling: true})
	}
	steps = append(steps, rep(OutcomeOK, 2)...)
	_, acts := run(t, State{}, steps)
	if count(acts, ActionDown) != 0 {
		t.Fatalf("a slow rollout alerted: %v", acts)
	}
	s, _ := run(t, State{}, []step{{o: OutcomeFail, rolling: true}, {o: OutcomeFail, rolling: true}, {o: OutcomeFail, rolling: true}})
	if s.Hold != HoldRollingOut {
		t.Fatalf("hold = %q, want rolling-out", s.Hold)
	}
	var long []step
	for i := 0; i < 20; i++ {
		long = append(long, step{o: OutcomeFail, rolling: true})
	}
	if _, acts := run(t, State{}, long); count(acts, ActionDown) != 1 {
		t.Fatalf("a rollout stuck past MaxHold never alerted: %v", acts)
	}
}
