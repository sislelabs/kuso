package alerts

import (
	"context"
	"fmt"
	"time"

	"kuso/server/internal/notify"
)

// Built-in platform checks that run every tick regardless of the user's
// rules: a Prometheus reachability check (rule errors were only ever
// logged, so a dead Prometheus silently disabled every HTTP rule), a
// cluster-wide "traffic stopped" check (the per-env HTTP rules skip
// envs below minReq, so a dead Traefik read as healthy silence) and an
// "every host answers 5xx" check (per-service 5xx rules are opt-in).
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
	// probeFloor (req/s) is traffic kuso generates itself: the uptime
	// watcher sends up to 3 requests a minute through Traefik's
	// ClusterIP. Those keep flowing when the public path (LB, firewall,
	// DNS) is broken, so a rate at or under this floor counts as zero.
	probeFloor = 0.075
	// trafficMinRate (req/s) is what counts as "had traffic" before the
	// drop. About 12 requests a minute including the probes: an idle or
	// near-idle cluster never qualifies, so it can't fire on a quiet night.
	trafficMinRate = 0.2
	// trafficZeroTicks: consecutive zero-traffic ticks before firing.
	// Longer than a Prometheus restart's scrape gap.
	trafficZeroTicks = 3
	// trafficRecoverRate is what counts as traffic flowing again, above
	// the probe floor.
	trafficRecoverRate = 0.01

	// activeServicesQuery / failingServicesQuery count Traefik services
	// with traffic, and those where most of it is 5xx. When every active
	// service is failing the cause is shared (Traefik, its middleware,
	// the network to the pods), not one app.
	activeServicesQuery  = `count(sum by (service) (rate(traefik_service_requests_total[5m])) > 0.01)`
	failingServicesQuery = `count((sum by (service) (rate(traefik_service_requests_total{code=~"5.."}[5m])) / sum by (service) (rate(traefik_service_requests_total[5m]))) > 0.5)`
	// all5xxMinServices: one app throwing 5xx is that app's problem.
	all5xxMinServices = 2
	all5xxTicks       = 3
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

	all5xxStreak int
	all5xxDown   bool
	all5xxAt     time.Time
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
// rule tick; never concurrently with itself. Every state flip happens
// only after its event was delivered, so a failed send (control-plane
// DB down and no channel reachable) is retried on the next tick.
func (e *Engine) platformTick(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	v, err := e.promScalar(qctx, trafficQuery)
	cancel()
	if ctx.Err() != nil {
		return // shutting down, not a Prometheus failure
	}
	st := &e.platform
	if err != nil {
		st.promFails++
		st.promLastErr = err.Error()
		if st.promFails >= promFailThreshold && !st.degraded &&
			e.emitDurable(notify.AlertEngineDegraded(st.promFails, st.promLastErr)) {
			st.degraded = true
			st.degradedAt = now
		}
		e.Logger.Warn("alerts: prometheus check failed", "consecutive", st.promFails, "err", err)
		// Traffic state is untouched: no data is not zero traffic.
		return
	}
	if st.degraded && e.emitDurable(notify.PlatformRecovered("alert engine (Prometheus)", now.Sub(st.degradedAt))) {
		st.degraded = false
	}
	st.promFails = 0

	switch {
	case v > probeFloor:
		if st.ingressDown && v >= probeFloor+trafficRecoverRate &&
			e.emitDurable(notify.PlatformRecovered("ingress traffic", now.Sub(st.downAt))) {
			st.ingressDown = false
		}
		// Light traffic lowers the baseline too, so a quiet night that
		// tapers off to zero doesn't look like a sudden stop.
		st.baseline = v
		st.zeroStreak = 0
	default:
		if st.baseline >= trafficMinRate {
			st.zeroStreak++
			if st.zeroStreak >= trafficZeroTicks && !st.ingressDown &&
				e.emitDurable(notify.IngressDown("traffic stopped",
					fmt.Sprintf("Requests through Traefik dropped from %.2f/s to zero across every host for %d minutes (kuso's own uptime probes excluded). Traefik, its load balancer or the public DNS/firewall path may be down.",
						st.baseline, st.zeroStreak))) {
				st.ingressDown = true
				st.downAt = now
			}
		}
	}
	e.all5xxTick(ctx, now)
}

// all5xxTick fires ingress.down when every service with traffic has
// answered mostly 5xx for all5xxTicks ticks in a row.
func (e *Engine) all5xxTick(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	active, aerr := e.promScalar(qctx, activeServicesQuery)
	failing, ferr := e.promScalar(qctx, failingServicesQuery)
	cancel()
	if aerr != nil || ferr != nil {
		return // unknown this tick
	}
	st := &e.platform
	if active >= all5xxMinServices && failing >= active {
		st.all5xxStreak++
		if st.all5xxStreak >= all5xxTicks && !st.all5xxDown &&
			e.emitDurable(notify.IngressDown("5xx on every host",
				fmt.Sprintf("All %d services with traffic have answered mostly 5xx for %d minutes. The cause is shared: Traefik, a middleware, or the network to the pods.",
					int(active), st.all5xxStreak))) {
			st.all5xxDown = true
			st.all5xxAt = now
		}
		return
	}
	st.all5xxStreak = 0
	if st.all5xxDown && e.emitDurable(notify.PlatformRecovered("ingress (5xx on every host)", now.Sub(st.all5xxAt))) {
		st.all5xxDown = false
	}
}

// emitDurable sends ev and reports whether it was delivered (outbox row
// or direct fallback).
func (e *Engine) emitDurable(ev notify.Event) bool {
	if e.emitDurableFn != nil {
		return e.emitDurableFn(ev) == nil
	}
	if e.emitFn != nil {
		e.emitFn(ev)
		return true
	}
	if e.Notify == nil {
		return false
	}
	if err := e.Notify.EmitDurable(ev); err != nil {
		e.Logger.Warn("alerts: platform event not delivered, retrying next tick", "type", string(ev.Type), "err", err)
		return false
	}
	return true
}
