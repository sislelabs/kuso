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
func TestObserveHelpers(t *testing.T) {
	start := time.Now().Add(-50 * time.Millisecond)

	ObserveBuildCreate(start, nil)
	ObserveBuildCreate(start, errors.New("boom"))
	if c := histSampleCount(t, buildCreateDuration, "ok"); c != 1 {
		t.Errorf("build_create ok count = %d, want 1", c)
	}
	if c := histSampleCount(t, buildCreateDuration, "error"); c != 1 {
		t.Errorf("build_create error count = %d, want 1", c)
	}

	ObserveWebhookDispatch("push", start, nil)
	if c := histSampleCount(t, webhookDispatchDuration, "push", "ok"); c != 1 {
		t.Errorf("webhook push ok count = %d, want 1", c)
	}
	// Empty event must not produce an empty label (it maps to "unknown").
	ObserveWebhookDispatch("", start, nil)
	if c := histSampleCount(t, webhookDispatchDuration, "unknown", "ok"); c != 1 {
		t.Errorf("webhook unknown ok count = %d, want 1", c)
	}

	ObserveReconcileObserve(start, nil)
	if c := histSampleCount(t, reconcileObserveDuration, "ok"); c != 1 {
		t.Errorf("reconcile observe ok count = %d, want 1", c)
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
