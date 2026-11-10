package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"kuso/server/internal/notify"
)

type scriptedProm struct {
	vals []float64
	errs []error
	i    int
}

func (s *scriptedProm) next(context.Context, string) (float64, error) {
	i := s.i
	s.i++
	if i < len(s.errs) && s.errs[i] != nil {
		return 0, s.errs[i]
	}
	if i < len(s.vals) {
		return s.vals[i], nil
	}
	return 0, nil
}

func newPlatformEngine(p *scriptedProm, got *[]notify.Event) *Engine {
	e := New(nil, nil, nil, nil, slogDiscard())
	e.promScalarFn = p.next
	e.emitFn = func(ev notify.Event) { *got = append(*got, ev) }
	return e
}

func runTicks(e *Engine, n int) {
	t0 := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		e.platformTick(context.Background(), t0.Add(time.Duration(i)*time.Minute))
	}
}

func countType(evs []notify.Event, typ notify.EventType) int {
	n := 0
	for _, e := range evs {
		if e.Type == typ {
			n++
		}
	}
	return n
}

// Traffic that drops from a real rate to zero fires once after the
// zero streak, and recovers once when it returns.
func TestPlatformTrafficDropFiresOnce(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{vals: []float64{5, 5, 0, 0, 0, 0, 0, 4}}
	e := newPlatformEngine(p, &got)
	runTicks(e, 4)
	if n := countType(got, notify.EventIngressDown); n != 0 {
		t.Fatalf("fired after 2 zero ticks, want none yet: %d", n)
	}
	runTicks(e, 3)
	if n := countType(got, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down count = %d, want exactly 1", n)
	}
	runTicks(e, 1)
	if n := countType(got, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("recovered count = %d, want 1", n)
	}
}

// A fresh process that starts while traffic is already zero (upgrade
// during an idle period, or an outage that predates this process) must
// not page: there is no baseline.
func TestPlatformZeroTrafficFromFirstTickNeverFires(t *testing.T) {
	var got []notify.Event
	e := newPlatformEngine(&scriptedProm{vals: []float64{0, 0, 0, 0, 0, 0}}, &got)
	runTicks(e, 6)
	if len(got) != 0 {
		t.Fatalf("got %d events on a cluster that never had traffic, want 0", len(got))
	}
}

// A near-idle cluster tapering to zero never had "meaningful" traffic.
func TestPlatformLightTrafficToZeroNeverFires(t *testing.T) {
	var got []notify.Event
	e := newPlatformEngine(&scriptedProm{vals: []float64{0.05, 0.02, 0, 0, 0, 0}}, &got)
	runTicks(e, 6)
	if len(got) != 0 {
		t.Fatalf("got %d events for light traffic tapering off, want 0", len(got))
	}
}

// Prometheus unreachable: one alert.engine_degraded after the threshold,
// not one per tick; a recovery when queries succeed again. Failed
// queries must not count as zero traffic.
func TestPlatformPrometheusDownEmitsOnce(t *testing.T) {
	var got []notify.Event
	boom := errors.New("connection refused")
	errs := make([]error, 12)
	vals := make([]float64, 13)
	vals[0] = 5
	for i := 1; i < 12; i++ {
		errs[i] = boom
	}
	vals[12] = 5
	e := newPlatformEngine(&scriptedProm{vals: vals, errs: errs}, &got)
	runTicks(e, promFailThreshold) // tick 0 ok + threshold-1 failures
	if n := countType(got, notify.EventAlertEngineDown); n != 0 {
		t.Fatalf("degraded fired before %d failures", promFailThreshold)
	}
	runTicks(e, 1)
	if n := countType(got, notify.EventAlertEngineDown); n != 1 {
		t.Fatalf("degraded count = %d after threshold, want 1", n)
	}
	runTicks(e, 6)
	if n := countType(got, notify.EventAlertEngineDown); n != 1 {
		t.Fatalf("degraded re-fired: count = %d, want 1", n)
	}
	if n := countType(got, notify.EventIngressDown); n != 0 {
		t.Fatalf("prometheus errors were read as zero traffic: %d ingress.down", n)
	}
	runTicks(e, 1)
	if n := countType(got, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("recovered count = %d, want 1", n)
	}
}
