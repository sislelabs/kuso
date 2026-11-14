package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"kuso/server/internal/notify"
)

type scriptedProm struct {
	// vals / errs answer trafficQuery, one entry per tick.
	vals []float64
	errs []error
	// quiet / steady answer quietQuery and steadyQuery for the same
	// tick; a missing entry reads as no traffic and no steady history.
	quiet, steady []float64
	i             int
	// active / failing answer the all-5xx check's two queries.
	active, failing float64
}

func (s *scriptedProm) next(_ context.Context, q string) (float64, error) {
	tick := s.i - 1
	at := func(xs []float64) float64 {
		if tick >= 0 && tick < len(xs) {
			return xs[tick]
		}
		return 0
	}
	switch q {
	case activeServicesQuery:
		return s.active, nil
	case failingServicesQuery:
		return s.failing, nil
	case quietQuery:
		return at(s.quiet), nil
	case steadyQuery:
		return at(s.steady), nil
	}
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

// Steady traffic followed by a quiet window fires once, and recovers
// once when requests come back.
func TestPlatformTrafficDropFiresOnce(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{
		vals:   []float64{5, 0, 0, 4},
		quiet:  []float64{5, 3, 0, 0},
		steady: []float64{30, 30, 30, 30},
	}
	e := newPlatformEngine(p, &got)
	runTicks(e, 2)
	if n := countType(got, notify.EventIngressDown); n != 0 {
		t.Fatalf("fired before the quiet window was empty: %d", n)
	}
	runTicks(e, 1)
	if n := countType(got, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down count = %d, want exactly 1", n)
	}
	runTicks(e, 1)
	if n := countType(got, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("recovered count = %d, want 1", n)
	}
}

// The live false alarm: a low-traffic cluster gets requests in bursts
// (a minute at 0.3 req/s, then several silent minutes). Silence after a
// burst is idle, not an outage, so it must not page however long the
// gap.
func TestPlatformBurstyTrafficSilenceNeverFires(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{
		vals:   []float64{0.42, 0, 0, 0, 0, 0},
		quiet:  []float64{0.3, 0.01, 0.01, 0, 0, 0},
		steady: []float64{14, 14, 13, 12, 12, 11},
	}
	e := newPlatformEngine(p, &got)
	runTicks(e, 6)
	if len(got) != 0 {
		t.Fatalf("bursty traffic going quiet paged: %v", got)
	}
}

// A fresh process that starts while traffic is already zero (upgrade
// during an idle period) has no steady history and must not page.
func TestPlatformZeroTrafficFromFirstTickNeverFires(t *testing.T) {
	var got []notify.Event
	e := newPlatformEngine(&scriptedProm{vals: []float64{0, 0, 0, 0, 0, 0}}, &got)
	runTicks(e, 6)
	if len(got) != 0 {
		t.Fatalf("got %d events on a cluster that never had traffic, want 0", len(got))
	}
}

// kuso's own through-Traefik uptime probes (~0.05 req/s) keep flowing
// when the public path is broken; they must not hide the drop.
func TestPlatformProbeOnlyTrafficCountsAsZero(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{
		vals:   []float64{0.05},
		quiet:  []float64{0.05},
		steady: []float64{30},
	}
	e := newPlatformEngine(p, &got)
	runTicks(e, 1)
	if n := countType(got, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down count = %d with only probe traffic left, want 1", n)
	}
}

// A page that couldn't be delivered (control-plane DB down, no channel
// reachable) is retried next tick instead of being marked sent.
func TestPlatformUndeliveredPageRetries(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{
		vals:   []float64{0, 0, 0},
		quiet:  []float64{0, 0, 0},
		steady: []float64{30, 30, 30},
	}
	e := newPlatformEngine(p, &got)
	fails := 1
	e.emitDurableFn = func(ev notify.Event) error {
		if fails > 0 {
			fails--
			return errors.New("outbox down")
		}
		got = append(got, ev)
		return nil
	}
	runTicks(e, 3) // fires on tick 1 (fails), retried on tick 2
	if n := countType(got, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down delivered %d times, want 1 (retry after a failed send)", n)
	}
}

// Every active service answering 5xx pages once; one failing app doesn't.
func TestPlatformAll5xx(t *testing.T) {
	var got []notify.Event
	p := &scriptedProm{vals: []float64{5, 5, 5, 5, 5, 5, 5}, active: 1, failing: 1}
	e := newPlatformEngine(p, &got)
	runTicks(e, 4)
	if len(got) != 0 {
		t.Fatalf("one failing service paged: %v", got)
	}
	p.active, p.failing = 3, 3
	runTicks(e, 2)
	if len(got) != 0 {
		t.Fatalf("paged before %d ticks", all5xxTicks)
	}
	runTicks(e, 3)
	if n := countType(got, notify.EventIngressDown); n != 1 {
		t.Fatalf("ingress.down count = %d, want 1", n)
	}
	p.failing = 1
	runTicks(e, 1)
	if n := countType(got, notify.EventPlatformRecovered); n != 1 {
		t.Fatalf("recovered count = %d, want 1", n)
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
