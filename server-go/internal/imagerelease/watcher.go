// Package imagerelease runs the pre-deploy release hook (migrations) for
// runtime=image services, which skip the build pipeline and so are never
// seen by the build poller's release path. It reconciles KusoEnvironments
// carrying a withheld spec.pendingImage: it runs the release Job against
// that image and, on success, promotes pendingImage→image (the chart then
// scales the held-at-0 pod up onto the migrated image). On failure the
// image stays withheld and the failure is surfaced.
package imagerelease

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
	"kuso/server/internal/serverstate"
)

// DefaultTickInterval is the watcher's tick cadence when Watcher.Tick is
// unset. Exported so main.go can register imagerelease in the serverstate
// liveness registry at the cadence it beats. main.go leaves Tick unset, so
// this is the effective interval.
const DefaultTickInterval = 15 * time.Second

// releaseTimeout bounds one detached release run. It sits above the release
// hook's own ceiling (spec.release.timeoutSeconds + 30s, default 930s), so
// it is a backstop against a wedged goroutine, not the primary timeout.
const releaseTimeout = 30 * time.Minute

// Runner is the release-Job runner (releaserun.Runner satisfies it).
type Runner interface {
	Run(ctx context.Context, ns string, env *kube.KusoEnvironment, image *kube.KusoImage) (releaserun.Result, error)
}

type Watcher struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger
	Tick      time.Duration
	Release   Runner
	// Notify is optional — a func to surface a release failure (bell/webhook).
	Notify func(project, service, msg string)

	// running holds the envs whose release is in flight, so later ticks
	// skip them instead of starting a duplicate run.
	mu      sync.Mutex
	running map[string]struct{}
	wg      sync.WaitGroup
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	tick := w.Tick
	if tick <= 0 {
		tick = DefaultTickInterval
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := w.reconcileOnce(ctx); err != nil {
				w.Logger.Error("imagerelease reconcile", "err", err)
			}
			serverstate.LoopHeartbeat(serverstate.LoopImageRelease)
		}
	}
}

// reconcileOnce lists envs with a withheld pendingImage + release hook and
// drives each through the release Job → promote/withhold decision.
func (w *Watcher) reconcileOnce(ctx context.Context) error {
	// Single-tenant: all env CRs live in w.Namespace (the kuso namespace).
	envs, err := w.Kube.ListKusoEnvironments(ctx, w.Namespace)
	if err != nil {
		return err
	}
	for i := range envs {
		e := &envs[i]
		if e.Spec.PendingImage == nil {
			continue
		}
		if e.Spec.Release == nil || len(e.Spec.Release.Command) == 0 {
			continue // shouldn't happen (we only set pendingImage with a hook) — skip defensively
		}
		if e.Spec.Kind == "preview" {
			continue
		}
		w.releaseAsync(ctx, e)
	}
	return nil
}

// releaseAsync runs one env's release hook and the promote/withhold decision
// off the tick goroutine. Release.Run blocks until the migration Job ends
// (up to 930s by default); inline, it stalled the loop's heartbeat and
// liveness restarted the leader mid-migration. The context is still a child
// of the loop's, so losing leadership cancels the run. A failed run leaves
// pendingImage set, so the next tick retries exactly as before.
func (w *Watcher) releaseAsync(ctx context.Context, e *kube.KusoEnvironment) {
	key := w.Namespace + "/" + e.Name
	w.mu.Lock()
	if w.running == nil {
		w.running = make(map[string]struct{})
	}
	if _, busy := w.running[key]; busy {
		w.mu.Unlock()
		return
	}
	w.running[key] = struct{}{}
	w.mu.Unlock()

	env := *e // e points into the caller's list, reused next tick
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer func() {
			w.mu.Lock()
			delete(w.running, key)
			w.mu.Unlock()
		}()
		rctx, cancel := context.WithTimeout(ctx, releaseTimeout)
		defer cancel()
		w.release(rctx, &env)
	}()
}

func (w *Watcher) release(ctx context.Context, e *kube.KusoEnvironment) {
	res, err := w.Release.Run(ctx, w.Namespace, e, e.Spec.PendingImage)
	if err != nil {
		w.Logger.Error("imagerelease: run", "env", e.Name, "err", err)
		return // transient — retry next tick (Job is idempotent per env,tag)
	}
	switch res.Outcome {
	case releaserun.OutcomeSucceeded:
		if err := w.promote(ctx, w.Namespace, e.Name, e.Spec.PendingImage); err != nil {
			w.Logger.Error("imagerelease: promote", "env", e.Name, "err", err)
			return
		}
		w.Logger.Info("imagerelease: promoted after release", "env", e.Name, "job", res.JobName)
	default: // Failed / TimedOut
		w.Logger.Warn("imagerelease: release failed, image withheld", "env", e.Name, "outcome", res.Outcome, "job", res.JobName)
		if w.Notify != nil {
			w.Notify(e.Spec.Project, e.Spec.Service, "release hook failed: "+res.Message)
		}
		// Leave pendingImage set. The per-(env,tag) Job name blocks a
		// re-run of the same tag until the user changes the image.
	}
}

// wait blocks until every in-flight release has finished. Tests only.
func (w *Watcher) wait() { w.wg.Wait() }

// promote sets Image=img and clears PendingImage via read-modify-write with
// retry (mirrors the build poller's promoteEnvImageCAS conflict handling).
func (w *Watcher) promote(ctx context.Context, ns, envName string, img *kube.KusoImage) error {
	_, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(env *kube.KusoEnvironment) error {
		env.Spec.Image = img
		env.Spec.PendingImage = nil
		return nil
	})
	return err
}
