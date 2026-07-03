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
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/releaserun"
)

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
}

func (w *Watcher) Run(ctx context.Context) {
	if w.Logger == nil {
		w.Logger = slog.Default()
	}
	tick := w.Tick
	if tick <= 0 {
		tick = 15 * time.Second
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
		res, err := w.Release.Run(ctx, w.Namespace, e, e.Spec.PendingImage)
		if err != nil {
			w.Logger.Error("imagerelease: run", "env", e.Name, "err", err)
			continue // transient — retry next tick (Job is idempotent per env,tag)
		}
		switch res.Outcome {
		case releaserun.OutcomeSucceeded:
			if err := w.promote(ctx, w.Namespace, e.Name, e.Spec.PendingImage); err != nil {
				w.Logger.Error("imagerelease: promote", "env", e.Name, "err", err)
				continue
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
	return nil
}

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
