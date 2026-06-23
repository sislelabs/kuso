package projects

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
//
// Resumable on partial failure: if a previous run deleted the env CR
// but errored before cleaning up addons/secrets, calling this again
// re-runs the orphan cleanup using label-based discovery instead of
// the (now-missing) env CR. The first thing every step does is
// idempotency-check; everything tolerates NotFound. Net effect: a
// caller can retry on any error without worrying about which phase
// failed.
func (s *Service) DeleteEnvironment(ctx context.Context, project, env string) error {
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return err
	}

	// Phase 1: resolve the env CR (if it still exists) so we can pull
	// the service FQN out of its spec. On a resumed delete the CR is
	// already gone — fall back to parsing the env name. Both paths feed
	// into the same downstream cleanup.
	var (
		serviceFQN string
		envKind    string
	)
	e, gerr := s.GetEnvironment(ctx, project, env)
	switch {
	case gerr == nil:
		if e.Spec.Kind == "production" {
			return fmt.Errorf("%w: cannot delete production environment %s", ErrInvalid, env)
		}
		serviceFQN = e.Spec.Service
		envKind = e.Spec.Kind
	case apierrors.IsNotFound(gerr) || errors.Is(gerr, ErrNotFound):
		// CR is gone — a prior run got past phase 2 but failed during
		// cleanup. Reconstruct what we can from the env name. We can't
		// verify Kind is "preview" any more, but a missing CR means
		// the prior run already passed that check, so proceeding is
		// safe.
		serviceFQN = inferServiceFQNFromEnv(env)
	default:
		return gerr
	}

	// Phase 2: delete the env CR itself. Idempotent — NotFound is OK
	// because either we just observed it missing above, or it was
	// raced away by another caller.
	if err := s.Kube.DeleteKusoEnvironment(ctx, ns, env); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete env: %w", err)
	}

	// Phase 3: tear down preview-DB clones tied to this PR. Label-based
	// discovery doesn't require the env CR to exist any more. Without
	// this, 100 PRs/day × occasional missed close-webhook = compounding
	// orphan StatefulSets + PVCs forever. Per-addon errors are tolerated
	// — the env is already gone, the addon will get reconciled on the
	// next sweep tick.
	if pr := previewPRNumber(env, serviceFQN); pr != "" {
		selector := kube.LabelSelector(map[string]string{
			kube.LabelProject:                  project,
			"kuso.sislelabs.com/preview-pr":    pr,
		})
		if addonList, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		}); lerr == nil {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					_ = derr
				}
			}
		}
	} else if scope := envScopeForDelete(e, env, project, serviceFQN); scope != "" {
		// Named env (staging/qa/...): delete every addon scoped to this env via the
		// canonical env label, so the env's OWN DB/redis/s3 + their PVCs are removed.
		selector := kube.LabelSelector(map[string]string{
			kube.LabelProject: project,
			kube.LabelEnv:     scope,
		})
		if addonList, lerr := s.Kube.Dynamic.Resource(kube.GVRAddons).Namespace(ns).List(ctx, metav1.ListOptions{
			LabelSelector: selector,
		}); lerr == nil {
			for i := range addonList.Items {
				name := addonList.Items[i].GetName()
				if derr := s.Kube.DeleteKusoAddon(ctx, ns, name); derr != nil && !apierrors.IsNotFound(derr) {
					_ = derr
				}
			}
		}
	}

	// Phase 4: wipe per-env Secret. Tolerant of NotFound; the secrets
	// service handles re-entry on its own. Suppressing the error here
	// is the existing behaviour and intentional — leaving an orphan
	// Secret is preferable to surfacing an error the operator can do
	// nothing about.
	if s.SecretsCleanupForEnv != nil && serviceFQN != "" {
		svcShort := strings.TrimPrefix(serviceFQN, project+"-")
		if svcShort == "" {
			svcShort = serviceFQN
		}
		envShort := strings.TrimPrefix(env, serviceFQN+"-")
		if envShort == env {
			envShort = env
		}
		if cerr := s.SecretsCleanupForEnv(ctx, project, svcShort, envShort); cerr != nil {
			_ = cerr
		}
	}
	// envKind is referenced for the production-env guard above; keep
	// the variable live so future cleanup phases that need to vary by
	// kind don't have to re-fetch.
	_ = envKind
	return nil
}

// inferServiceFQNFromEnv reconstructs the service FQN when the env CR
// is already gone. Convention: env names are "<service-fqn>-<suffix>"
// where suffix is either "production" or "pr-<N>". Strip the suffix
// to get the FQN. Returns "" if neither suffix matches — in that case
// downstream label-based cleanup is a no-op (no preview-pr label to
// match) and the per-env Secret cleanup skips.
func inferServiceFQNFromEnv(env string) string {
	if i := strings.LastIndex(env, "-pr-"); i > 0 {
		return env[:i]
	}
	if strings.HasSuffix(env, "-production") {
		return strings.TrimSuffix(env, "-production")
	}
	return ""
}

// envScopeForDelete returns the env's kuso.sislelabs.com/env scope value (the env
// short name, e.g. "staging") used to find its per-env addon clones on delete.
// Prefers the CR's own env label when present; otherwise derives it from the env
// CR name ("<service-fqn>-<scope>"). Returns "" when it can't be determined (the
// addon sweep is then skipped — orphan-tolerant, like the rest of the cleanup).
func envScopeForDelete(e *kube.KusoEnvironment, env, project, serviceFQN string) string {
	if e != nil {
		if scope := e.Labels[kube.LabelEnv]; scope != "" {
			return scope
		}
	}
	if serviceFQN != "" {
		suffix := strings.TrimPrefix(env, serviceFQN+"-")
		if suffix != env && suffix != "production" {
			return suffix
		}
	}
	return ""
}

// previewPRNumber extracts the PR number from a preview env CR name.
// Convention: env name = "<service-fqn>-pr-<N>". Returns "" when
// the env isn't a preview (production envs end in "-production").
func previewPRNumber(env, serviceFQN string) string {
	suffix := strings.TrimPrefix(env, serviceFQN+"-")
	if suffix == env {
		// no service prefix on the env name; can't tell.
		return ""
	}
	if !strings.HasPrefix(suffix, "pr-") {
		return ""
	}
	return strings.TrimPrefix(suffix, "pr-")
}
