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
	"fmt"
	"log/slog"
	"strconv"
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

// Annotations recording a failed release on the env CR, so a broken
// migration is retried with backoff and then left alone instead of being
// re-run against production every tick.
const (
	AnnFailedImage    = "kuso.sislelabs.com/release-failed-image"
	AnnFailedAttempts = "kuso.sislelabs.com/release-failed-attempts"
	AnnFailedAt       = "kuso.sislelabs.com/release-failed-at"
)

// maxAttempts is how many times one pending image's release hook runs
// before the watcher stops retrying it. A new image resets the count.
const maxAttempts = 3

// retryBackoff is the wait after the n-th failure (n >= 1) before the next
// attempt.
func retryBackoff(n int) time.Duration {
	if n <= 1 {
		return 5 * time.Minute
	}
	return 30 * time.Minute
}

func imageKey(img *kube.KusoImage) string {
	return img.Repository + ":" + img.Tag
}

type Watcher struct {
	Kube      *kube.Client
	Namespace string
	// Namespaces lists every namespace holding env CRs (home plus each
	// project's own namespace). nil = Namespace only.
	Namespaces func(ctx context.Context) []string
	Logger     *slog.Logger
	Tick       time.Duration
	Release    Runner
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
	nss := []string{w.Namespace}
	if w.Namespaces != nil {
		nss = w.Namespaces(ctx)
	}
	var firstErr error
	for _, ns := range nss {
		envs, err := w.Kube.ListKusoEnvironments(ctx, ns)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
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
			if !w.dueForAttempt(e, time.Now()) {
				continue
			}
			w.releaseAsync(ctx, e)
		}
	}
	return firstErr
}

// dueForAttempt reports whether the env's pending image may run its release
// hook now: always for an image that hasn't failed, after a backoff for one
// that has, and never once it has failed maxAttempts times.
func (w *Watcher) dueForAttempt(e *kube.KusoEnvironment, now time.Time) bool {
	if e.Annotations[AnnFailedImage] != imageKey(e.Spec.PendingImage) {
		return true
	}
	n, _ := strconv.Atoi(e.Annotations[AnnFailedAttempts])
	if n >= maxAttempts {
		return false
	}
	at, err := time.Parse(time.RFC3339, e.Annotations[AnnFailedAt])
	if err != nil {
		return true
	}
	return !now.Before(at.Add(retryBackoff(n)))
}

// releaseAsync runs one env's release hook and the promote/withhold decision
// off the tick goroutine. Release.Run blocks until the migration Job ends
// (up to 930s by default); inline, it stalled the loop's heartbeat and
// liveness restarted the leader mid-migration. The context is still a child
// of the loop's, so losing leadership cancels the run. A failed run leaves
// pendingImage set, so the next tick retries exactly as before.
func (w *Watcher) releaseAsync(ctx context.Context, e *kube.KusoEnvironment) {
	key := e.Namespace + "/" + e.Name
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
	ns := e.Namespace
	if ns == "" {
		ns = w.Namespace
	}
	res, err := w.Release.Run(ctx, ns, e, e.Spec.PendingImage)
	if err != nil {
		w.Logger.Error("imagerelease: run", "env", e.Name, "err", err)
		return // transient — retry next tick (Job is idempotent per env,tag)
	}
	switch res.Outcome {
	case releaserun.OutcomeSucceeded:
		if err := w.promote(ctx, ns, e.Name, e.Spec.PendingImage); err != nil {
			w.Logger.Error("imagerelease: promote", "env", e.Name, "err", err)
			return
		}
		w.Logger.Info("imagerelease: promoted after release", "env", e.Name, "job", res.JobName)
	default: // Failed / TimedOut
		attempts, err := w.recordFailure(ctx, ns, e.Name, e.Spec.PendingImage)
		if err != nil {
			w.Logger.Error("imagerelease: record failure", "env", e.Name, "err", err)
		}
		w.Logger.Warn("imagerelease: release failed, image withheld", "env", e.Name, "outcome", res.Outcome, "job", res.JobName, "attempt", attempts)
		if w.Notify != nil {
			next := fmt.Sprintf("retrying in %s (attempt %d of %d)", retryBackoff(attempts), attempts, maxAttempts)
			if attempts >= maxAttempts {
				next = fmt.Sprintf("giving up after %d attempts; set a new image to retry", attempts)
			}
			w.Notify(e.Spec.Project, e.Spec.Service, fmt.Sprintf("release hook failed for %s: %s; %s", e.Spec.PendingImage.Tag, res.Message, next))
		}
	}
}

// recordFailure stamps the failed image and attempt count on the env so
// dueForAttempt can back off. Returns the attempt count after this failure.
func (w *Watcher) recordFailure(ctx context.Context, ns, envName string, img *kube.KusoImage) (int, error) {
	attempts := 1
	_, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(env *kube.KusoEnvironment) error {
		if env.Annotations == nil {
			env.Annotations = map[string]string{}
		}
		attempts = 1
		if env.Annotations[AnnFailedImage] == imageKey(img) {
			n, _ := strconv.Atoi(env.Annotations[AnnFailedAttempts])
			attempts = n + 1
		}
		env.Annotations[AnnFailedImage] = imageKey(img)
		env.Annotations[AnnFailedAttempts] = strconv.Itoa(attempts)
		env.Annotations[AnnFailedAt] = time.Now().UTC().Format(time.RFC3339)
		return nil
	})
	return attempts, err
}

// wait blocks until every in-flight release has finished. Tests only.
func (w *Watcher) wait() { w.wg.Wait() }

// promote sets Image=img via read-modify-write with retry (mirrors the build
// poller's promoteEnvImageCAS conflict handling). PendingImage is cleared
// only when it still holds img: an image set while this release ran hasn't
// been migrated for, so it stays pending for the next tick.
func (w *Watcher) promote(ctx context.Context, ns, envName string, img *kube.KusoImage) error {
	_, err := w.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envName, func(env *kube.KusoEnvironment) error {
		env.Spec.Image = img
		if env.Spec.PendingImage != nil && imageKey(env.Spec.PendingImage) == imageKey(img) {
			env.Spec.PendingImage = nil
		}
		delete(env.Annotations, AnnFailedImage)
		delete(env.Annotations, AnnFailedAttempts)
		delete(env.Annotations, AnnFailedAt)
		return nil
	})
	return err
}
