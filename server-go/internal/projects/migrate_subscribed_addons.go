package projects

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"kuso/server/internal/kube"
)

// MigrateLegacySubscribedAddons seeds spec.SubscribedAddons on every
// existing service from the project's addon list, so the upgrade is
// zero-churn. Idempotent + bounded: services already migrated are
// skipped.
//
// Called once on leader startup, after MigrateLegacySharedEnvKeys (so
// both fields are settled before the env-CR pod rolls).
func (s *Service) MigrateLegacySubscribedAddons(ctx context.Context, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	projects, err := s.List(ctx)
	if err != nil {
		logger.WarnContext(ctx, "subscribed-addons migration: list projects failed",
			"err", err)
		return
	}
	migrated, skipped, failed := 0, 0, 0
	for _, p := range projects {
		ns, err := s.namespaceFor(ctx, p.Name)
		if err != nil {
			logger.WarnContext(ctx, "subscribed-addons migration: namespace lookup failed",
				"project", p.Name, "err", err)
			continue
		}
		shortAddons := s.collectAvailableAddonShortNames(ctx, p.Name)
		svcs, err := s.ListServices(ctx, p.Name)
		if err != nil {
			logger.WarnContext(ctx, "subscribed-addons migration: list services failed",
				"project", p.Name, "err", err)
			continue
		}
		for i := range svcs {
			svc := &svcs[i]
			if svc.Spec.SubscribedAddons != nil {
				skipped++
				continue
			}
			short := shortServiceName(p.Name, svc.Name)
			if _, err := s.migrateOneServiceAddons(ctx, ns, p.Name, short, shortAddons); err != nil {
				failed++
				logger.WarnContext(ctx, "subscribed-addons migration: service failed",
					"project", p.Name, "service", svc.Name, "err", err)
				continue
			}
			migrated++
			logger.InfoContext(ctx, "subscribed-addons migration: service migrated",
				"project", p.Name, "service", svc.Name, "addons", len(shortAddons))
		}
	}
	logger.InfoContext(ctx, "subscribed-addons migration complete",
		"migrated", migrated, "skipped", skipped, "failed", failed)
}

// collectAvailableAddonShortNames returns the SHORT addon names
// (without "<project>-" prefix and "-conn" suffix) for every addon
// in the project. Empty when no AddonConnSecrets resolver is wired.
func (s *Service) collectAvailableAddonShortNames(ctx context.Context, project string) []string {
	full := s.listProjectAddonConnSecrets(ctx, project)
	out := make([]string, 0, len(full))
	for _, name := range full {
		s := strings.TrimSuffix(name, "-conn")
		s = strings.TrimPrefix(s, project+"-")
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// migrateOneServiceAddons patches a single service's SubscribedAddons
// to the supplied list + runs propagation. Holds the per-service lock.
func (s *Service) migrateOneServiceAddons(ctx context.Context, ns, project, shortService string, addons []string) (*kube.KusoService, error) {
	mu := s.lockService(project, shortService)
	defer mu.Unlock()
	fresh, err := s.GetService(ctx, project, shortService)
	if err != nil {
		return nil, fmt.Errorf("re-fetch service: %w", err)
	}
	if fresh.Spec.SubscribedAddons != nil {
		return fresh, nil
	}
	// RMW under optimistic concurrency; re-check the already-migrated
	// guard against the fresh object so a racing migrator can't double-set.
	updated, err := s.Kube.UpdateKusoServiceWithRetry(ctx, ns, serviceCRName(project, shortService), func(svc *kube.KusoService) error {
		if svc.Spec.SubscribedAddons != nil {
			return kube.ErrAbortRetry
		}
		svc.Spec.SubscribedAddons = append([]string{}, addons...)
		return nil
	})
	if err != nil {
		if errors.Is(err, kube.ErrAbortRetry) {
			return fresh, nil
		}
		return nil, fmt.Errorf("update service: %w", err)
	}
	if err := s.propagateChangedToEnvs(ctx, ns, project, shortService, updated, changedFields{EnvVars: true}); err != nil {
		return updated, nil
	}
	return updated, nil
}
