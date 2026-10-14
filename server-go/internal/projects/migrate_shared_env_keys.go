package projects

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strings"

	"kuso/server/internal/instancesecrets"
	"kuso/server/internal/kube"
)

// shortServiceName strips the "<project>-" prefix from a KusoService
// CR name so GetService / lockService (which take the short name)
// don't 404. Mirrors the convention used elsewhere in this package.
func shortServiceName(project, fqn string) string {
	prefix := project + "-"
	if strings.HasPrefix(fqn, prefix) {
		return strings.TrimPrefix(fqn, prefix)
	}
	return fqn
}

// MigrateLegacySharedEnvKeys walks every service in every project and
// seeds spec.sharedEnvKeys for services where it is still nil
// (pre-v0.16.11 "legacy mode"). The seed is the union of keys present
// in <project>-shared + kuso-instance-shared at migration time, so the
// next reconcile mounts exactly the same set of keys those pods
// already had — zero behavioral change on the flip.
//
// B2.7 caveat: this preserves runtime behavior but is conservative —
// every service ends up subscribed to every shared key, even keys it
// never actually read. The audit recommends trimming subscriptions
// post-migration by walking the pod's env dump and dropping unused
// keys, but that requires live pod introspection and is deferred to a
// post-v0.17.1 cleanup pass. For now, users can hand-trim
// spec.sharedEnvKeys in the env-vars panel — the subscription chip
// shows exactly which keys are wired.
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
				if hasAdminKeySubscription(svc.Spec.SharedEnvKeys, availableKeys) {
					short := shortServiceName(p.Name, svc.Name)
					if err := s.dropAdminKeySubscriptions(ctx, ns, p.Name, short, availableKeys); err != nil {
						logger.WarnContext(ctx, "shared-env-keys migration: dropping instance admin DSN subscription failed",
							"project", p.Name, "service", svc.Name, "err", err)
					} else {
						logger.InfoContext(ctx, "shared-env-keys migration: dropped instance admin DSN subscription",
							"project", p.Name, "service", svc.Name)
					}
				}
				skipped++
				continue
			}
			short := shortServiceName(p.Name, svc.Name)
			if _, err := s.migrateOneService(ctx, ns, p.Name, short, availableKeys); err != nil {
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
			if hiddenSharedKey(name, k) {
				continue
			}
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
// SetEnv / PatchService doesn't race. shortService is the user-facing
// short name (no "<project>-" prefix), required by GetService and
// lockService.
func (s *Service) migrateOneService(ctx context.Context, ns, project, shortService string, keys []string) (*kube.KusoService, error) {
	mu := s.lockService(project, shortService)
	defer mu.Unlock()

	// Re-fetch under the lock so we don't write a stale spec back.
	fresh, err := s.GetService(ctx, project, shortService)
	if err != nil {
		return nil, fmt.Errorf("re-fetch service: %w", err)
	}
	if fresh.Spec.SharedEnvKeys != nil {
		// Won the race against another writer — nothing to do.
		return fresh, nil
	}
	// RMW under optimistic concurrency; also re-checks the already-migrated
	// guard against the fresh object so a concurrent migrator can't double-set.
	updated, err := s.Kube.UpdateKusoServiceWithRetry(ctx, ns, serviceCRName(project, shortService), func(svc *kube.KusoService) error {
		if svc.Spec.SharedEnvKeys != nil {
			return kube.ErrAbortRetry
		}
		svc.Spec.SharedEnvKeys = append([]string{}, keys...)
		return nil
	})
	if err != nil {
		if errors.Is(err, kube.ErrAbortRetry) {
			return fresh, nil
		}
		return nil, fmt.Errorf("update service: %w", err)
	}
	if err := s.propagateChangedToEnvs(ctx, ns, project, shortService, updated, changedFields{EnvVars: true}); err != nil {
		// Best-effort: service spec is the source of truth, next
		// edit will retry propagation.
		return updated, nil
	}
	return updated, nil
}

// seedSharedEnvKeys persists the mount-all equivalent subscription on a
// service whose spec.sharedEnvKeys is still nil (created after the last
// boot migration, or by a path that doesn't seed). Propagation always
// converts the shared-secret mounts into per-key refs, so running it on a
// nil list stripped every shared var from the pod; seeding first keeps
// exactly the keys the pod already had (minus instance-addon admin DSNs).
func (s *Service) seedSharedEnvKeys(ctx context.Context, ns, project string, svc *kube.KusoService) (*kube.KusoService, error) {
	if svc.Spec.SharedEnvKeys != nil || s.Kube.Clientset == nil {
		return svc, nil
	}
	keys, err := s.collectAvailableSharedKeys(ctx, ns, project)
	if err != nil {
		return nil, err
	}
	updated, err := s.Kube.UpdateKusoServiceWithRetry(ctx, ns, svc.Name, func(cur *kube.KusoService) error {
		if cur.Spec.SharedEnvKeys != nil {
			return kube.ErrAbortRetry
		}
		cur.Spec.SharedEnvKeys = append([]string{}, keys...)
		return nil
	})
	if errors.Is(err, kube.ErrAbortRetry) {
		return s.Kube.GetKusoService(ctx, ns, svc.Name)
	}
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// hasAdminKeySubscription reports a subscription to an instance-addon admin
// DSN key that only kuso-instance-shared provides (available excludes those).
// Earlier migrations seeded every instance key, so every service migrated
// after an instance PG was registered carries the superuser DSN.
func hasAdminKeySubscription(keys, available []string) bool {
	return slices.ContainsFunc(keys, func(k string) bool {
		return instancesecrets.IsAdminDSNKey(k) && !slices.Contains(available, k)
	})
}

// dropAdminKeySubscriptions removes those keys from spec.sharedEnvKeys and
// propagates, which also drops the stale secretKeyRef from every env CR.
func (s *Service) dropAdminKeySubscriptions(ctx context.Context, ns, project, shortService string, available []string) error {
	mu := s.lockService(project, shortService)
	defer mu.Unlock()
	updated, err := s.Kube.UpdateKusoServiceWithRetry(ctx, ns, serviceCRName(project, shortService), func(svc *kube.KusoService) error {
		if !hasAdminKeySubscription(svc.Spec.SharedEnvKeys, available) {
			return kube.ErrAbortRetry
		}
		svc.Spec.SharedEnvKeys = slices.DeleteFunc(slices.Clone(svc.Spec.SharedEnvKeys), func(k string) bool {
			return instancesecrets.IsAdminDSNKey(k) && !slices.Contains(available, k)
		})
		return nil
	})
	if errors.Is(err, kube.ErrAbortRetry) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("update service: %w", err)
	}
	return s.propagateChangedToEnvs(ctx, ns, project, shortService, updated, changedFields{EnvVars: true})
}
