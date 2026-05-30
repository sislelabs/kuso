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

// healthz is the liveness probe. It returns 200 unconditionally as
// long as the process is up enough to serve HTTP. The version field
// is what hack/install.sh and the GH-release post-deploy probe
// (hack/release.sh) compare to confirm a rollout took.
//
// Intentionally minimal — no DB / kube checks here. A liveness probe
// failing on a transient dependency outage would have the kubelet
// restart the pod, which makes the outage worse. Use /readyz for
// "fit to serve traffic" semantics.
func healthz(w http.ResponseWriter, _ *http.Request) {
	body, _ := json.Marshal(map[string]string{
		"status":  "ok",
		"version": version.Version(),
	})
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
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

		// Build-poller liveness — only meaningful on the leader (only the
		// leader runs the poller). A dead/panicked poller goroutine stops
		// stamping its heartbeat; the leader then goes unready so the LB
		// drains it and it's eventually recycled, releasing the lease to
		// a healthy replica. maxStale = 6× the 5s tick: tolerant of a
		// slow tick / brief apiserver hiccup, fast enough to catch a
		// genuinely stuck poller within ~30s. Non-leaders always pass.
		if healthy, leading := serverstate.PollerHealthy(30 * time.Second); leading && !healthy {
			checks["buildPoller"] = "stalled"
			ready = false
			if d.Logger != nil {
				d.Logger.Error("readyz: build poller heartbeat stale — leader poller may be dead")
			}
		} else if leading {
			checks["buildPoller"] = "ok"
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
