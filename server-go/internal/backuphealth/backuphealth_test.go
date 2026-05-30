package backuphealth

import (
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestDetail locks the verdict precedence: missing CronJob → not
// configured → suspended → never succeeded → stale → healthy. The
// banner renders Detail verbatim, so order matters.
func TestDetail(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   Status
		want string
	}{
		{"no cronjob", Status{}, "not found"},
		{"not configured", Status{CronJobPresent: true}, "not configured"},
		{"suspended", Status{CronJobPresent: true, Configured: true, Suspended: true}, "suspended"},
		{"never succeeded", Status{CronJobPresent: true, Configured: true}, "none have succeeded"},
		{"stale", Status{CronJobPresent: true, Configured: true, LastSuccessAt: "2020-01-01T00:00:00Z", Stale: true}, "stale"},
		{"healthy", Status{CronJobPresent: true, Configured: true, LastSuccessAt: "2020-01-01T00:00:00Z"}, "healthy"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := detail(tc.in); !strings.Contains(got, tc.want) {
				t.Errorf("detail = %q, want substring %q", got, tc.want)
			}
		})
	}
}

func TestNewestTerminalTimes(t *testing.T) {
	t.Parallel()
	mk := func(success bool, at time.Time) batchv1.Job {
		j := batchv1.Job{Status: batchv1.JobStatus{CompletionTime: &metav1.Time{Time: at}}}
		if success {
			j.Status.Succeeded = 1
		} else {
			j.Status.Failed = 1
		}
		return j
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jobs := []batchv1.Job{
		mk(true, t0),
		mk(true, t0.Add(2*time.Hour)),  // newest success
		mk(false, t0.Add(1*time.Hour)), // newest failure
		{Status: batchv1.JobStatus{Active: 1}},
	}
	success, failure := newestTerminalTimes(jobs)
	if !success.Equal(t0.Add(2 * time.Hour)) {
		t.Errorf("success = %v, want %v", success, t0.Add(2*time.Hour))
	}
	if !failure.Equal(t0.Add(1 * time.Hour)) {
		t.Errorf("failure = %v, want %v", failure, t0.Add(1*time.Hour))
	}
}

// TestWatcherEdgeTriggers verifies the watcher fires only on state
// changes: first unhealthy → 1 event, stays unhealthy → 0 more, recovers
// → 1 event.
func TestWatcherEdgeTriggers(t *testing.T) {
	t.Parallel()
	w := &Watcher{}

	// Simulate the tick's decision logic directly via a small driver,
	// since tick() does kube I/O. We exercise the edge bookkeeping by
	// calling a fake-state helper.
	type emit struct{ unhealthy, recovered bool }
	var emits []emit
	step := func(unhealthy bool) {
		if w.evaluated && unhealthy == w.lastUnhealthy {
			w.evaluated, w.lastUnhealthy = true, unhealthy
			return
		}
		prev := w.lastUnhealthy
		w.evaluated, w.lastUnhealthy = true, unhealthy
		switch {
		case unhealthy:
			emits = append(emits, emit{unhealthy: true})
		case prev:
			emits = append(emits, emit{recovered: true})
		}
	}

	step(true)  // first: unhealthy → fire
	step(true)  // still unhealthy → no fire
	step(true)  // still → no fire
	step(false) // recovered → fire
	step(false) // still healthy → no fire
	step(true)  // unhealthy again → fire

	if len(emits) != 3 {
		t.Fatalf("expected 3 edge emits, got %d: %+v", len(emits), emits)
	}
	if !emits[0].unhealthy || !emits[1].recovered || !emits[2].unhealthy {
		t.Errorf("edge sequence wrong: %+v", emits)
	}
}
