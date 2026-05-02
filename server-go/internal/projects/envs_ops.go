package projects

import (
	"context"
	"fmt"
	"time"

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

// SweepExpiredPreviews scans every preview KusoEnvironment in the
// configured namespace and deletes any whose spec.ttl.expiresAt is in
// the past. Webhooks are the primary teardown mechanism; this is the
// safety net for missed close events / suspended Apps / past outages.
//
// Returns the number of envs deleted. Errors against individual envs
// are logged via the supplied callback (or swallowed when nil) so one
// flaky teardown doesn't stop the sweep.
func (s *Service) SweepExpiredPreviews(ctx context.Context, onErr func(name string, err error)) (int, error) {
	envs, err := s.Kube.ListKusoEnvironments(ctx, s.Namespace)
	if err != nil {
		return 0, err
	}
	now := time.Now().UTC()
	deleted := 0
	for _, e := range envs {
		if e.Spec.Kind != "preview" || e.Spec.TTL == nil || e.Spec.TTL.ExpiresAt == "" {
			continue
		}
		exp, err := time.Parse(time.RFC3339, e.Spec.TTL.ExpiresAt)
		if err != nil || !exp.Before(now) {
			continue
		}
		if err := s.Kube.DeleteKusoEnvironment(ctx, s.Namespace, e.Name); err != nil {
			if onErr != nil {
				onErr(e.Name, err)
			}
			continue
		}
		deleted++
	}
	return deleted, nil
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
