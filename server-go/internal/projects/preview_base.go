package projects

import (
	"context"
	"fmt"

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
)

// requirePreviewBaseEnv refuses to turn previews on for a project with no
// env a preview could be based on: a "staging" env (plain or env group) or
// a non-production env a preview trigger names. Previews copy that env's
// config and secrets and never production's, so without one every PR would
// be refused (github.resolvePreviewBase applies the same rule per service).
func (s *Service) requirePreviewBaseEnv(ctx context.Context, proj *kube.KusoProject) error {
	ns, err := s.namespaceFor(ctx, proj.Name)
	if err != nil {
		return err
	}
	bases := map[string]bool{"staging": true}
	if proj.Spec.Previews != nil {
		for _, t := range proj.Spec.Previews.Triggers {
			if t.BaseEnv != "" && t.BaseEnv != "production" {
				bases[t.BaseEnv] = true
			}
		}
	}
	envs, err := s.Kube.ListKusoEnvironmentsByLabels(ctx, ns, map[string]string{labelProject: proj.Name})
	if err != nil {
		return fmt.Errorf("list envs of %s: %w", proj.Name, err)
	}
	for i := range envs {
		if bases[envs[i].Labels[labelEnv]] && !addons.IsPreviewEnv(&envs[i]) {
			return nil
		}
	}
	return fmt.Errorf("%w: previews need a staging environment to copy config and secrets from (they never copy production's); create a staging environment first", ErrInvalid)
}
