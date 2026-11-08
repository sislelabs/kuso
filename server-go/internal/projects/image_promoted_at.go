package projects

import (
	"context"
	"log/slog"

	"kuso/server/internal/kube"
)

// needsPromotedAtBackfill reports whether a runtime=image env serves an
// image but has no promoted-at stamp (envs created before image services
// stamped one).
func needsPromotedAtBackfill(env *kube.KusoEnvironment) bool {
	return env.Spec.Runtime == "image" &&
		env.Spec.Image != nil && env.Spec.Image.Repository != "" &&
		env.Annotations[kube.AnnPromotedAt] == ""
}

// HealImagePromotedAt is the boot-time sweep that gives runtime=image envs
// created before the stamp existed a promoted-at, so they show a deploy
// time. The env's creation time is the best record left: these envs are
// born with their image. Idempotent; failures are logged.
func (s *Service) HealImagePromotedAt(ctx context.Context, logger *slog.Logger) {
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		logger.Warn("promoted-at heal: list projects failed", "err", err)
		return
	}
	healed := 0
	for i := range projects {
		project := projects[i].Name
		ns, nerr := s.namespaceFor(ctx, project)
		if nerr != nil {
			logger.Warn("promoted-at heal: resolve namespace failed", "project", project, "err", nerr)
			continue
		}
		envs, lerr := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{labelProject: project})
		if lerr != nil {
			logger.Warn("promoted-at heal: list envs failed", "project", project, "err", lerr)
			continue
		}
		for j := range envs {
			if !needsPromotedAtBackfill(&envs[j]) {
				continue
			}
			_, uerr := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, ns, envs[j].Name, func(env *kube.KusoEnvironment) error {
				if needsPromotedAtBackfill(env) {
					kube.StampPromotedAt(env, env.CreationTimestamp.Time)
				}
				return nil
			})
			if uerr != nil {
				logger.Warn("promoted-at heal: update env failed", "env", envs[j].Name, "err", uerr)
				continue
			}
			healed++
		}
	}
	if healed > 0 {
		logger.Info("promoted-at heal: stamped image envs", "count", healed)
	}
}
