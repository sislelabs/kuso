// Per-service addon-conn subscription. See KusoService.spec.SubscribedAddons.
//
// Design mirror of shared_env_keys.go but for addon secrets. Before
// v0.16.23 every project addon's <addon>-conn secret was blanket-
// mounted on every service, leaking DATABASE_URL / REDIS_URL etc.
// into pods that never read them. Now each service explicitly
// subscribes to the addons it wants; the kuso server filters
// envFromSecrets at propagation time.

package projects

import (
	"context"
	"sort"
	"strings"

	"kuso/server/internal/kube"
)

// filterEnvFromForSubscription returns envFromSecrets with addon-conn
// entries pruned down to only those in the subscription list. Non-
// addon-conn entries (project-shared, instance-shared, env-scoped
// secrets, etc.) pass through unchanged. When subscribedAddons is nil
// the input is returned as-is (legacy auto-mount-all).
//
// projectAddons is the full list of <addon>-conn secret names that
// belong to the project; we use it to distinguish "addon conn this
// project owns" from "some other -conn secret" (e.g. a user's
// external Secret named foo-conn). Without that, the filter would
// accidentally strip user-created secrets.
func filterEnvFromForSubscription(envFromSecrets []string, subscribedAddons []string, projectAddons []string, project string) []string {
	if subscribedAddons == nil {
		return envFromSecrets
	}
	// Build the allow-set of conn-secret names from subscribed addon
	// short names. Conn secrets follow the "<project>-<addon>-conn"
	// convention (services_ops.go:AddonConnSecrets), so we accept BOTH
	// shapes the user might write: short ("pg" → "<project>-pg-conn")
	// and FQ ("tickero-pg" → "tickero-pg-conn").
	allow := make(map[string]bool, len(subscribedAddons))
	for _, name := range subscribedAddons {
		// FQ form: already has the project prefix.
		allow[name+"-conn"] = true
		// Short form: needs the project prefix.
		if project != "" {
			allow[project+"-"+name+"-conn"] = true
		}
	}
	// Set of all project-owned conn-secret names. Anything NOT in
	// this set passes through unchanged.
	projectAddonSet := make(map[string]bool, len(projectAddons))
	for _, name := range projectAddons {
		projectAddonSet[name] = true
	}

	out := make([]string, 0, len(envFromSecrets))
	for _, sec := range envFromSecrets {
		// Always keep secrets that don't look like a project addon
		// conn-secret. Covers project-shared, kuso-instance-shared,
		// per-service secrets, env-scoped secrets, and user-added
		// foo-conn secrets that aren't an addon.
		if !projectAddonSet[sec] {
			out = append(out, sec)
			continue
		}
		// Project addon conn-secret — gated by the allow-list.
		if allow[sec] {
			out = append(out, sec)
		}
	}
	return out
}

// listProjectAddonConnSecrets resolves the full list of addon-conn
// secret names owned by the project, using the wired AddonConnSecrets
// callback. Returns the empty slice when the callback isn't wired
// (test harnesses + early-boot) so the filter degrades to a no-op
// rather than stripping every secret.
func (s *Service) listProjectAddonConnSecrets(ctx context.Context, project string) []string {
	if s.AddonConnSecrets == nil {
		return nil
	}
	secs, err := s.AddonConnSecrets(ctx, project)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(secs))
	for _, sec := range secs {
		if strings.HasSuffix(sec, "-conn") {
			out = append(out, sec)
		}
	}
	sort.Strings(out)
	return out
}

// SubscribableAddons is the response shape for the GET endpoint that
// powers the dashboard's "addon mounts" chip toggle. Subscribed is
// the service's current list; Available is the full project addon
// set (so the UI can render outline chips for unsubscribed addons).
type SubscribableAddons struct {
	Subscribed []string `json:"subscribed"`
	Available  []string `json:"available"`
}

// ListSubscribableAddons returns the (subscribed, available) pair so
// the dashboard can render a chip per addon with the right state.
// Names are short-form ("pg" not "tickero-pg") for UI consumption.
func (s *Service) ListSubscribableAddons(ctx context.Context, project, service string) (*SubscribableAddons, error) {
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	available := s.listProjectAddonConnSecrets(ctx, project)
	// Strip "-conn" + project prefix to get short addon names.
	shortAvailable := make([]string, 0, len(available))
	for _, full := range available {
		short := strings.TrimSuffix(full, "-conn")
		short = strings.TrimPrefix(short, project+"-")
		shortAvailable = append(shortAvailable, short)
	}
	sort.Strings(shortAvailable)

	subscribed := svc.Spec.SubscribedAddons
	if subscribed == nil {
		// Legacy mode — coerce to [] for the wire so the UI doesn't
		// have to special-case nil. The post-migration server seeds
		// every service so this branch only fires for pre-migration
		// reads.
		subscribed = []string{}
	}
	return &SubscribableAddons{
		Subscribed: subscribed,
		Available:  shortAvailable,
	}, nil
}

// SetSubscribedAddons replaces the subscription list outright and
// re-propagates envFromSecrets to every env of the service.
func (s *Service) SetSubscribedAddons(ctx context.Context, project, service string, addons []string) (*kube.KusoService, error) {
	if addons == nil {
		addons = []string{}
	}
	mu := s.lockService(project, service)
	defer mu.Unlock()

	// Normalize: trim + dedupe + sort. Empty entries dropped.
	seen := make(map[string]bool, len(addons))
	clean := make([]string, 0, len(addons))
	for _, a := range addons {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		clean = append(clean, a)
	}
	sort.Strings(clean)

	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return nil, err
	}
	svc, err := s.GetService(ctx, project, service)
	if err != nil {
		return nil, err
	}
	svc.Spec.SubscribedAddons = clean
	updated, err := s.Kube.UpdateKusoService(ctx, ns, svc)
	if err != nil {
		return nil, err
	}
	if err := s.propagateChangedToEnvs(ctx, ns, project, service, updated, changedFields{EnvVars: true}); err != nil {
		// Best-effort propagation; service spec is the source of
		// truth, next save retries.
		return updated, nil
	}
	return updated, nil
}
