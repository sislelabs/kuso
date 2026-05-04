package projects

import (
	"context"
	"fmt"
	"strings"
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
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	e, err := s.Kube.GetKusoEnvironment(ctx, ns, env)
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
	// Build the set of namespaces to scan: home + every distinct
	// spec.namespace declared by a KusoProject. Dedupe so we don't
	// double-sweep the home ns when a project is unset.
	projects, err := s.Kube.ListKusoProjects(ctx, s.Namespace)
	if err != nil {
		return 0, err
	}
	seen := map[string]bool{s.Namespace: true}
	nss := []string{s.Namespace}
	for _, p := range projects {
		ns := p.Spec.Namespace
		if ns == "" || seen[ns] {
			continue
		}
		seen[ns] = true
		nss = append(nss, ns)
	}

	now := time.Now().UTC()
	deleted := 0
	for _, ns := range nss {
		envs, err := s.Kube.ListKusoEnvironments(ctx, ns)
		if err != nil {
			if onErr != nil {
				onErr("ns:"+ns, err)
			}
			continue
		}
		for _, e := range envs {
			if e.Spec.Kind != "preview" || e.Spec.TTL == nil || e.Spec.TTL.ExpiresAt == "" {
				continue
			}
			exp, err := time.Parse(time.RFC3339, e.Spec.TTL.ExpiresAt)
			if err != nil || !exp.Before(now) {
				continue
			}
			if err := s.Kube.DeleteKusoEnvironment(ctx, ns, e.Name); err != nil {
				if onErr != nil {
					onErr(e.Name, err)
				}
				continue
			}
			// Drop the cache for the env's project so the next
			// Describe doesn't return a freshly-deleted preview.
			if proj := e.Labels[labelProject]; proj != "" {
				s.invalidateDescribe(proj)
			}
			deleted++
		}
	}
	return deleted, nil
}

// DeleteEnvironment removes a preview env. Production envs cannot be
// deleted directly — service deletion handles those. Mirrors the TS
// behaviour because preview teardown is the legitimate use case here.
//
// We also wipe the per-env Secret (the helm-operator's finalizer tears
// down the helm release but leaves the underlying Secret CR), so
// repeated PR open/close cycles don't accumulate orphan
// <project>-<service>-<env>-secrets in the namespace.
func (s *Service) DeleteEnvironment(ctx context.Context, project, env string) error {
	e, err := s.GetEnvironment(ctx, project, env)
	if err != nil {
		return err
	}
	if e.Spec.Kind == "production" {
		return fmt.Errorf("%w: cannot delete production environment %s", ErrInvalid, env)
	}
	defer s.invalidateDescribe(project)
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}
	if err := s.Kube.DeleteKusoEnvironment(ctx, ns, env); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete env: %w", err)
	}
	if s.SecretsCleanupForEnv != nil {
		// Service short name = env CR's spec.service stripped of the
		// "<project>-" prefix. Env short name = env CR name with the
		// service FQN prefix removed (e.g. "myproj-web-pr-7" → "pr-7").
		svcShort := strings.TrimPrefix(e.Spec.Service, project+"-")
		if svcShort == "" {
			svcShort = e.Spec.Service
		}
		envShort := strings.TrimPrefix(env, e.Spec.Service+"-")
		if envShort == env {
			// env CR name didn't carry the service prefix — fall back
			// to using the raw env name. The secrets sanitiser will
			// produce a stable name regardless.
			envShort = env
		}
		if err := s.SecretsCleanupForEnv(ctx, project, svcShort, envShort); err != nil {
			// Log via the kube client's logger if available, otherwise
			// swallow — the env CR is already gone, so leaving an
			// orphan Secret is preferable to surfacing a cleanup error
			// the user can do nothing about.
			_ = err
		}
	}
	return nil
}
