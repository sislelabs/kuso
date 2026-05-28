package projects

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	"kuso/server/internal/kube"
)

// MigrateLegacySharedEnvKeys walks every service in every project and
// seeds spec.sharedEnvKeys for services where it is still nil
// (pre-v0.16.11 "legacy mode"). The seed is the union of keys present
// in <project>-shared + kuso-instance-shared at migration time, so the
// next reconcile mounts exactly the same set of keys those pods
// already had — zero behavioral change on the flip.
//
// Idempotent: services that already have a non-nil list are skipped.
// Errors on individual services are logged and skipped; the migration
// returns nil even if some services failed so a single misbehaving CR
// can't block server startup. Each successful service triggers the
// usual EnvVars propagation, which re-stamps env CRs with the new
// per-key envVars + pruned envFromSecrets.
//
// Called once from main.go on server startup, after the kube client is
// wired and before the http server begins serving requests.
func (s *Service) MigrateLegacySharedEnvKeys(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	projects, err := s.List(ctx)
	if err != nil {
		logger.WarnContext(ctx, "shared-env-keys migration: list projects failed",
			"err", err)
		return
	}
	migrated, skipped, failed := 0, 0, 0
	for _, p := range projects {
		ns, err := s.namespaceFor(ctx, p.Name)
		if err != nil {
			logger.WarnContext(ctx, "shared-env-keys migration: namespace lookup failed",
				"project", p.Name, "err", err)
			continue
		}
		// Resolve the available keys ONCE per project — every service
		// in this project gets the same seed.
		availableKeys, err := s.collectAvailableSharedKeys(ctx, ns, p.Name)
		if err != nil {
			logger.WarnContext(ctx, "shared-env-keys migration: list secret keys failed",
				"project", p.Name, "err", err)
			continue
		}
		svcs, err := s.ListServices(ctx, p.Name)
		if err != nil {
			logger.WarnContext(ctx, "shared-env-keys migration: list services failed",
				"project", p.Name, "err", err)
			continue
		}
		for i := range svcs {
			svc := &svcs[i]
			if svc.Spec.SharedEnvKeys != nil {
				skipped++
				continue
			}
			if _, err := s.migrateOneService(ctx, ns, p.Name, svc, availableKeys); err != nil {
				failed++
				logger.WarnContext(ctx, "shared-env-keys migration: service failed",
					"project", p.Name, "service", svc.Name, "err", err)
				continue
			}
			migrated++
			logger.InfoContext(ctx, "shared-env-keys migration: service migrated",
				"project", p.Name, "service", svc.Name, "keys", len(availableKeys))
		}
	}
	logger.InfoContext(ctx, "shared-env-keys migration complete",
		"migrated", migrated, "skipped", skipped, "failed", failed)
}

// collectAvailableSharedKeys returns the dedup'd union of keys
// present in project-shared + instance-shared secrets. Either may be
// absent — empty set is a valid result.
func (s *Service) collectAvailableSharedKeys(ctx context.Context, ns, project string) ([]string, error) {
	seen := map[string]bool{}
	out := []string{}
	for _, name := range kube.SharedSecretNames(project) {
		keys, err := s.listSecretKeys(ctx, ns, name)
		if err != nil {
			return nil, fmt.Errorf("list keys for %s: %w", name, err)
		}
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// migrateOneService patches svc.Spec.SharedEnvKeys to keys + runs the
// usual env propagation. Holds the per-service lock so a concurrent
// SetEnv / PatchService doesn't race.
func (s *Service) migrateOneService(ctx context.Context, ns, project string, svc *kube.KusoService, keys []string) (*kube.KusoService, error) {
	mu := s.lockService(project, svc.Name)
	defer mu.Unlock()

	// Re-fetch under the lock so we don't write a stale spec back.
	fresh, err := s.GetService(ctx, project, svc.Name)
	if err != nil {
		return nil, fmt.Errorf("re-fetch service: %w", err)
	}
	if fresh.Spec.SharedEnvKeys != nil {
		// Won the race against another writer — nothing to do.
		return fresh, nil
	}
	fresh.Spec.SharedEnvKeys = append([]string{}, keys...)
	updated, err := s.Kube.UpdateKusoService(ctx, ns, fresh)
	if err != nil {
		return nil, fmt.Errorf("update service: %w", err)
	}
	if err := s.propagateChangedToEnvs(ctx, ns, project, svc.Name, updated, changedFields{EnvVars: true}); err != nil {
		// Best-effort: service spec is the source of truth, next
		// edit will retry propagation. Log already happens in
		// propagateChangedToEnvs.
		return updated, nil
	}
	return updated, nil
}
