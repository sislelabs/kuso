package http

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"kuso/server/internal/serverstate"
	"kuso/server/internal/version"
)

// Poller-heartbeat staleness thresholds. Both probes read the same
// clock (serverstate.PollerStuckFor) but apply very different windows:
//
//   - readyzPollerMaxStale (30s = 6× the 5s poller tick): a leader whose
//     poller misses a handful of beats goes UNREADY — the LB drains it
//     but the pod keeps running. Tolerant of a slow tick or a brief
//     apiserver hiccup; catches a genuinely stuck poller within ~30s.
//
//   - healthzPollerHardStuck (5min = 60× the tick, 10× the readiness
//     window): only a poller that has been silent this long fails
//     LIVENESS, and only then does the kubelet restart the pod (which
//     finally releases the leader lease — readiness failure alone never
//     restarts anything, so a wedged replicas=1 leader used to blackhole
//     the API while holding the lease forever). The window is deliberately
//     huge relative to the tick because a false liveness failure is
//     itself an outage: with the deploy's livenessProbe config
//     (period 30s × failureThreshold 6) an actual restart lands ~3min
//     after the first failure, ~8min after the poller's last sign of
//     life. Nothing short of a hard-wedged goroutine survives 5 minutes
//     without stamping a 5s-cadence heartbeat.
//
// The never-beat-yet case measures from leadership start (see
// serverstate.PollerStuckFor), so these same windows double as the
// startup grace: a fresh leader has 30s to land its first beat before
// unready, 5min before a restart.
const (
	readyzPollerMaxStale   = 30 * time.Second
	healthzPollerHardStuck = 5 * time.Minute
)

// Per-loop staleness thresholds for the singleton-loop heartbeat registry
// (nodewatch, alerts, remediate, samplers, …). Unlike the build poller —
// which has one fixed 5s tick and so gets fixed windows above — these loops
// tick anywhere from 15s (imagerelease) to 15min (backuphealth), so their
// thresholds are DERIVED from each loop's own interval instead of a global
// constant. A slow loop must not read as stuck at a fast loop's window.
//
//   - Readiness: unready once a loop is stale beyond readyzLoopStaleMult ×
//     its interval (min readyzLoopStaleFloor). Modest multiple → drains the
//     pod from the LB on a genuinely stuck loop within a few missed ticks,
//     while tolerating one slow tick / brief apiserver hiccup.
//
//   - Liveness: 503 only once a loop is HARD-stuck — stale beyond
//     healthzLoopHardMult × its interval OR the healthzLoopHardFloor,
//     whichever is LARGER. The floor keeps a fast 15s loop from tripping
//     liveness on a ~2.5min blip (a false liveness failure is itself an
//     outage); the multiple keeps a slow 15min loop from being declared
//     stuck at the floor (10× → ~2.5h before a 15min loop restarts the pod).
//     A 5-min sampler lands at max(50min, 5min)=50min — nowhere near a
//     false positive at one missed 5-min tick.
//
// The never-beat-yet case is measured from loop registration (see
// serverstate.LoopStaleness), so these windows double as the per-loop
// startup grace exactly as they do for the poller.
const (
	readyzLoopStaleMult  = 6
	readyzLoopStaleFloor = 30 * time.Second
	healthzLoopHardMult  = 10
	healthzLoopHardFloor = 5 * time.Minute
)

// pollerStuckFor is indirected so probe tests can simulate a wedged
// leader clock without waiting minutes of wall time. loopStaleness is
// likewise indirected for the singleton-loop registry.
var (
	pollerStuckFor = serverstate.PollerStuckFor
	loopStaleness  = serverstate.LoopStaleness
)

// readyzLoopStale reports the readiness staleness window for a loop of the
// given tick interval: readyzLoopStaleMult × interval, floored.
func readyzLoopStale(interval time.Duration) time.Duration {
	if w := time.Duration(readyzLoopStaleMult) * interval; w > readyzLoopStaleFloor {
		return w
	}
	return readyzLoopStaleFloor
}

// healthzLoopHardStuck reports the liveness hard-stuck window for a loop of
// the given tick interval: the LARGER of healthzLoopHardMult × interval and
// the healthzLoopHardFloor. The floor guards fast loops from a false
// restart; the multiple guards slow loops from a premature one.
func healthzLoopHardStuck(interval time.Duration) time.Duration {
	if w := time.Duration(healthzLoopHardMult) * interval; w > healthzLoopHardFloor {
		return w
	}
	return healthzLoopHardFloor
}

// healthz is the liveness probe. It returns 200 as long as the process
// is up enough to serve HTTP — with ONE exception: a leader whose build
// poller has been silent past healthzPollerHardStuck is reported dead
// (503) so the kubelet restarts the pod and the leader lease is
// released to a healthy replica. The version field is what
// hack/install.sh and the GH-release post-deploy probe (hack/release.sh)
// compare to confirm a rollout took, so it is present on BOTH the 200
// and the 503 body.
//
// Otherwise intentionally minimal — no DB / kube checks here. A
// liveness probe failing on a transient dependency outage would have
// the kubelet restart the pod, which makes the outage worse. Use
// /readyz for "fit to serve traffic" semantics. In particular the
// CRD-stale degraded mode (serverstate.CRDStale) is a deliberate,
// flagged state — it fails readyz so the banner surfaces, but it MUST
// stay live so an operator can log in and see it; it is not consulted
// here and never can be: liveness keys only off the poller heartbeat
// clock, and degraded mode doesn't touch that clock.
func healthz(w http.ResponseWriter, _ *http.Request) {
	status, code := "ok", http.StatusOK
	if stuck, leading := pollerStuckFor(); leading && stuck > healthzPollerHardStuck {
		status, code = "stuck", http.StatusServiceUnavailable
	}
	// Singleton-loop registry (leader-gated: empty unless this pod holds
	// the relevant lease). Any registered loop hard-stuck past its own
	// per-interval window fails liveness too, so a wedged nodewatch /
	// alerts / sampler restarts the pod and releases the lease — the same
	// escalation the poller gets. Per-loop thresholds so a slow loop isn't
	// falsely flagged (see healthzLoopHardStuck). Degraded-mode / non-
	// leader stay live: neither registers loops, so the slice is empty.
	for _, ls := range loopStaleness() {
		if ls.StaleFor > healthzLoopHardStuck(ls.Interval) {
			status, code = "stuck", http.StatusServiceUnavailable
			break
		}
	}
	body, _ := json.Marshal(map[string]string{
		"status":  status,
		"version": version.Version(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(body)
}

// readyz returns 200 only when the dependencies kuso-server actually
// needs to serve traffic are healthy: DB reachable + kube informer
// cache synced (when the cache is enabled). Each check has a 1s
// budget — readiness probes run every few seconds and a slow probe
// pins the kube control plane.
//
// Response shape:
//
//	{"status":"ok"|"unready", "checks":{"db":"ok","kube":"ok"|"syncing"|"err: ..."}}
func readyz(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{}
		ready := true

		if d.DB != nil {
			ctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			if err := d.DB.PingContext(ctx); err != nil {
				// Generic body — readyz is on the public router and
				// raw Postgres errors leak the DSN host/user. Real
				// detail goes to slog where it stays inside the pod.
				checks["db"] = "unavailable"
				ready = false
				if d.Logger != nil {
					d.Logger.Warn("readyz: db ping failed", "err", err)
				}
			} else {
				checks["db"] = "ok"
			}
		}

		// Cache is optional — one-shot CLI runs disable it. When wired,
		// we require AllSynced before declaring ready so the LB doesn't
		// route to a pod whose informer hasn't done its initial list
		// (cold reads would fall back to the live API and amplify the
		// boot-time apiserver hit).
		if d.Kube != nil && d.Kube.Cache != nil {
			if d.Kube.Cache.AllSynced() {
				checks["kube"] = "ok"
			} else {
				checks["kube"] = "syncing"
				ready = false
			}
		}

		// CRD-stale gate. Set at boot when the schema preflight finds
		// fields this build expects that the live CRDs don't carry.
		// We come up unready (LB drains) AND surface the field list on
		// the body so an operator with `curl /readyz` sees exactly what
		// to re-apply, while the SPA can still load (read paths work)
		// and show its banner.
		if info := serverstate.CRDStale(); info != nil && len(info.Mismatches) > 0 {
			checks["crd"] = "stale: " + strings.Join(info.Mismatches, "; ")
			ready = false
		}

		// Build-poller readiness — only meaningful on the leader (only the
		// leader runs the poller). A dead/panicked poller goroutine stops
		// stamping its heartbeat; the leader then goes unready so the LB
		// drains it. readyzPollerMaxStale = 6× the 5s tick: tolerant of a
		// slow tick / brief apiserver hiccup, fast enough to catch a
		// genuinely stuck poller within ~30s. Non-leaders always pass.
		// Draining alone never RESTARTS the pod — the escalation to an
		// actual restart (which releases the lease) is healthz's
		// healthzPollerHardStuck window above.
		if stuck, leading := pollerStuckFor(); leading && stuck > readyzPollerMaxStale {
			checks["buildPoller"] = "stalled"
			ready = false
			if d.Logger != nil {
				d.Logger.Error("readyz: build poller heartbeat stale — leader poller may be dead", "stuckFor", stuck.String())
			}
		} else if leading {
			checks["buildPoller"] = "ok"
		}

		// Singleton-loop registry readiness — leader-gated (empty on a
		// non-leader). Each registered loop stale past its own per-interval
		// readiness window drains the pod from the LB; the escalation to an
		// actual restart is the healthz hard-stuck window above. Per-loop
		// thresholds so a 15min backuphealth loop and a 15s imagerelease
		// loop are each judged against their own cadence.
		loopsOK := true
		for _, ls := range loopStaleness() {
			if ls.StaleFor > readyzLoopStale(ls.Interval) {
				checks["loop:"+ls.Name] = "stalled"
				ready = false
				loopsOK = false
				if d.Logger != nil {
					d.Logger.Error("readyz: singleton loop heartbeat stale — loop may be dead",
						"loop", ls.Name, "stuckFor", ls.StaleFor.String(), "interval", ls.Interval.String())
				}
			}
		}
		if loopsOK {
			if stale := loopStaleness(); len(stale) > 0 {
				checks["loops"] = "ok"
			}
		}

		status := "ok"
		code := http.StatusOK
		if !ready {
			status = "unready"
			code = http.StatusServiceUnavailable
		}
		body, _ := json.Marshal(map[string]any{
			"status": status,
			"checks": checks,
		})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write(body)
	}
}
