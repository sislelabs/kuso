package projects

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"kuso/server/internal/kube"
)

// ListEnvironments returns the environments in a project (label-filtered).
func (s *Service) ListEnvironments(ctx context.Context, project string) ([]kube.KusoEnvironment, error) {
	return s.listEnvsForProject(ctx, project)
}

// GetEnvironment loads one environment by name.
func (s *Service) GetEnvironment(ctx context.Context, project, env string) (*kube.KusoEnvironment, error) {
	e, err := s.Kube.GetKusoEnvironment(ctx, s.Namespace, env)
	if apierrors.IsNotFound(err) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if e.Spec.Project != project {
		// Don't leak cross-project envs even if the URL is guessed.
		return nil, ErrNotFound
	}
	return e, nil
}

// DeleteEnvironment removes a preview env. Production envs cannot be
// deleted directly — service deletion handles those. Mirrors the TS
// behaviour because preview teardown is the legitimate use case here.
func (s *Service) DeleteEnvironment(ctx context.Context, project, env string) error {
	e, err := s.GetEnvironment(ctx, project, env)
	if err != nil {
		return err
	}
	if e.Spec.Kind == "production" {
		return fmt.Errorf("%w: cannot delete production environment %s", ErrInvalid, env)
	}
	if err := s.Kube.DeleteKusoEnvironment(ctx, s.Namespace, env); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete env: %w", err)
	}
	return nil
}
