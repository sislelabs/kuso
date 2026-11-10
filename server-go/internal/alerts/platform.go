package alerts

import (
	"context"
	"fmt"
	"time"

	"kuso/server/internal/notify"
)

// Built-in platform checks that run every tick regardless of the user's
// rules: a Prometheus reachability check (rule errors were only ever
// logged, so a dead Prometheus silently disabled every HTTP rule) and a
// cluster-wide "traffic stopped" check (the per-env HTTP rules skip
// envs below minReq, so a dead Traefik read as healthy silence).
//
// State lives in memory. A fresh process (upgrade, leader handover)
// starts with no baseline, so the first tick only seeds it and nothing
// fires until a later tick sees a real transition.

const (
	// promFailThreshold: consecutive failed ticks before
	// alert.engine_degraded. Five minutes rides out a Prometheus roll.
	promFailThreshold = 5
	// trafficQuery is total request rate across every Traefik service.
	trafficQuery = `sum(rate(traefik_service_requests_total[2m]))`
	// trafficMinRate (req/s) is what counts as "had traffic" before the
	// drop. About 6 requests a minute: an idle or near-idle cluster
	// never qualifies, so it can't fire on a quiet night.
	trafficMinRate = 0.1
	// trafficZeroTicks: consecutive zero-traffic ticks before firing.
	// Longer than a Prometheus restart's scrape gap.
	trafficZeroTicks = 3
	// trafficRecoverRate is what counts as traffic flowing again.
	trafficRecoverRate = 0.01
)

type platformState struct {
	promFails   int
	promLastErr string
	degraded    bool
	degradedAt  time.Time

	// baseline is the last observed non-zero traffic rate before the
	// current zero streak; 0 = none observed yet in this process.
	baseline    float64
	zeroStreak  int
	ingressDown bool
	downAt      time.Time
}

// promScalar runs an instant query whose result is a single unlabelled
// sample. An empty result (no series) reads as 0: that is what a sum
// over vanished Traefik series returns.
func (e *Engine) promScalar(ctx context.Context, q string) (float64, error) {
	if e.promScalarFn != nil {
		return e.promScalarFn(ctx, q)
	}
	vec, err := e.promVector(ctx, q)
	if err != nil {
		return 0, err
	}
	var total float64
	for _, v := range vec {
		total += v
	}
	return total, nil
}

// platformTick runs the built-in checks once. Called from Run after the
// rule tick; never concurrently with itself.
func (e *Engine) platformTick(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	v, err := e.promScalar(qctx, trafficQuery)
	cancel()
	if ctx.Err() != nil {
		return // shutting down, not a Prometheus failure
	}
	var events []notify.Event
	st := &e.platform
	if err != nil {
		st.promFails++
		st.promLastErr = err.Error()
		if st.promFails >= promFailThreshold && !st.degraded {
			st.degraded = true
			st.degradedAt = now
			events = append(events, notify.AlertEngineDegraded(st.promFails, st.promLastErr))
		}
		e.Logger.Warn("alerts: prometheus check failed", "consecutive", st.promFails, "err", err)
		// Traffic state is untouched: no data is not zero traffic.
		e.emitAll(events)
		return
	}
	if st.degraded {
		events = append(events, notify.PlatformRecovered("alert engine (Prometheus)", now.Sub(st.degradedAt)))
		st.degraded = false
	}
	st.promFails = 0

	switch {
	case v > 0:
		if st.ingressDown && v >= trafficRecoverRate {
			events = append(events, notify.PlatformRecovered("ingress traffic", now.Sub(st.downAt)))
			st.ingressDown = false
		}
		// Light traffic lowers the baseline too, so a quiet night that
		// tapers off to zero doesn't look like a sudden stop.
		st.baseline = v
		st.zeroStreak = 0
	default:
		if st.baseline >= trafficMinRate {
			st.zeroStreak++
			if st.zeroStreak >= trafficZeroTicks && !st.ingressDown {
				st.ingressDown = true
				st.downAt = now
				events = append(events, notify.IngressDown("traffic stopped",
					fmt.Sprintf("Requests through Traefik dropped from %.2f/s to zero across every host for %d minutes. Traefik may be down or unreachable.",
						st.baseline, st.zeroStreak)))
			}
		}
	}
	e.emitAll(events)
}

func (e *Engine) emitAll(events []notify.Event) {
	for _, ev := range events {
		if e.emitFn == nil && e.Notify == nil {
			continue
		}
		e.emit(ev)
	}
}
