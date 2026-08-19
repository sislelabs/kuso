package alerts

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// Concurrency + timeout tests for the perf-W8 fix: rules used to
// evaluate sequentially inside the 1-minute tick, so one pathological
// log-match rule could push the tick past its interval and starve
// every other rule. evalRules now runs a bounded worker pool with a
// per-rule context timeout. These tests are pure (no Postgres): they
// drive evalRules directly through the evaluateFn test seam.

func testRules(n int) []db.AlertRule {
	rules := make([]db.AlertRule, n)
	for i := range rules {
		rules[i] = db.AlertRule{ID: fmt.Sprintf("r%d", i), Name: fmt.Sprintf("rule %d", i), Enabled: true}
	}
	return rules
}

// TestEvalRulesBoundedConcurrency: 12 rules × 30ms with 4 workers must
// (a) all evaluate, (b) never exceed 4 in flight, (c) finish well under
// the 360ms a sequential pass would take.
func TestEvalRulesBoundedConcurrency(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slogDiscard(), workers: 4, evalTimeout: 5 * time.Second}
	var inFlight, peak, evaluated atomic.Int32
	e.evaluateFn = func(ctx context.Context, r *db.AlertRule, now time.Time) (bool, string, error) {
		c := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if c <= p || peak.CompareAndSwap(p, c) {
				break
			}
		}
		time.Sleep(30 * time.Millisecond)
		evaluated.Add(1)
		return false, "", nil
	}

	start := time.Now()
	e.evalRules(context.Background(), testRules(12), time.Now().UTC())
	elapsed := time.Since(start)

	if got := evaluated.Load(); got != 12 {
		t.Errorf("evaluated %d rules, want 12", got)
	}
	if got := peak.Load(); got > 4 {
		t.Errorf("peak concurrency %d exceeded worker bound 4", got)
	}
	if got := peak.Load(); got < 2 {
		t.Errorf("peak concurrency %d — rules did not run in parallel at all", got)
	}
	// Sequential would be ≥360ms; 4 workers ≈ 90ms. Generous headroom
	// for CI jitter, but strictly below sequential.
	if elapsed >= 300*time.Millisecond {
		t.Errorf("evalRules took %v — looks sequential (12×30ms)", elapsed)
	}
}

// TestEvalRulesPerRuleTimeout: a rule that blocks forever is cut off by
// the per-rule context and must not hold up the tick or the other
// rules. The blocked rule sees context.DeadlineExceeded.
func TestEvalRulesPerRuleTimeout(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slogDiscard(), workers: 2, evalTimeout: 50 * time.Millisecond}
	var fastDone atomic.Int32
	var slowErr atomic.Value
	e.evaluateFn = func(ctx context.Context, r *db.AlertRule, now time.Time) (bool, string, error) {
		if r.ID == "slow" {
			// Simulates a wedged log-match query: honours ctx like the
			// real DB driver does, but never finishes on its own.
			<-ctx.Done()
			slowErr.Store(ctx.Err())
			return false, "", ctx.Err()
		}
		fastDone.Add(1)
		return false, "", nil
	}
	rules := []db.AlertRule{{ID: "slow", Name: "wedged", Enabled: true}}
	rules = append(rules, testRules(3)...)

	start := time.Now()
	e.evalRules(context.Background(), rules, time.Now().UTC())
	elapsed := time.Since(start)

	if got := fastDone.Load(); got != 3 {
		t.Errorf("fast rules evaluated = %d, want 3 (slow rule starved them)", got)
	}
	if elapsed > time.Second {
		t.Errorf("evalRules took %v — per-rule timeout (50ms) not enforced", elapsed)
	}
	err, _ := slowErr.Load().(error)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("slow rule saw %v, want context.DeadlineExceeded", err)
	}
}

// TestEvalRulesEmptyAndDefaults: zero rules is a no-op, and the
// zero-value Engine falls back to the package defaults without
// panicking (the production Engine is built by New, which doesn't set
// the override fields).
func TestEvalRulesEmptyAndDefaults(t *testing.T) {
	t.Parallel()
	e := New(nil, nil, nil, nil, slogDiscard())
	e.evalRules(context.Background(), nil, time.Now().UTC())

	var n atomic.Int32
	e.evaluateFn = func(ctx context.Context, r *db.AlertRule, now time.Time) (bool, string, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("rule ctx has no deadline — per-rule timeout missing")
		}
		n.Add(1)
		return false, "", nil
	}
	e.evalRules(context.Background(), testRules(2), time.Now().UTC())
	if n.Load() != 2 {
		t.Errorf("evaluated %d, want 2", n.Load())
	}
}
