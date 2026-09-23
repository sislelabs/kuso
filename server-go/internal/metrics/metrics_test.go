package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// histSampleCount collects a HistogramVec child by its label values and
// returns its observation count. Histograms aren't usable with
// testutil.ToFloat64 (they emit multiple series), so read the count off
// the dto.Metric directly.
func histSampleCount(t *testing.T, vec *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	obs, err := vec.GetMetricWithLabelValues(labels...)
	if err != nil {
		t.Fatalf("GetMetricWithLabelValues(%v): %v", labels, err)
	}
	var m dto.Metric
	if err := obs.(prometheus.Metric).Write(&m); err != nil {
		t.Fatalf("write metric: %v", err)
	}
	if m.Histogram == nil {
		return 0
	}
	return m.Histogram.GetSampleCount()
}

// TestObserveHelpers verifies the latency-histogram helpers record a
// sample under the right outcome label without panicking — the call
// sites pass a raw error, so the ok/error split must follow it.
//
// The collectors are package globals, so assert on the change in count
// rather than the absolute value; otherwise -count=2 or -shuffle fails.
func TestObserveHelpers(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)

	type series struct {
		name   string
		vec    *prometheus.HistogramVec
		labels []string
	}
	buildOK := series{"build_create ok", buildCreateDuration, []string{"ok"}}
	buildErr := series{"build_create error", buildCreateDuration, []string{"error"}}
	pushOK := series{"webhook push ok", webhookDispatchDuration, []string{"push", "ok"}}
	unknownOK := series{"webhook unknown ok", webhookDispatchDuration, []string{"unknown", "ok"}}
	reconcileOK := series{"reconcile observe ok", reconcileObserveDuration, []string{"ok"}}
	all := []series{buildOK, buildErr, pushOK, unknownOK, reconcileOK}

	before := map[string]uint64{}
	for _, s := range all {
		before[s.name] = histSampleCount(t, s.vec, s.labels...)
	}

	ObserveBuildCreate(start, nil)
	ObserveBuildCreate(start, errors.New("boom"))
	ObserveWebhookDispatch("push", start, nil)
	// Empty event must not produce an empty label (it maps to "unknown").
	ObserveWebhookDispatch("", start, nil)
	ObserveReconcileObserve(start, nil)

	for _, s := range all {
		if d := histSampleCount(t, s.vec, s.labels...) - before[s.name]; d != 1 {
			t.Errorf("%s count grew by %d, want 1", s.name, d)
		}
	}
}

func TestOutcomeLabel(t *testing.T) {
	if got := outcome(nil); got != "ok" {
		t.Errorf("outcome(nil) = %q, want ok", got)
	}
	if got := outcome(errors.New("x")); got != "error" {
		t.Errorf("outcome(err) = %q, want error", got)
	}
}
