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

// rescopeAddonConnRefs rewrites explicit secretKeyRef env-vars that point at a
// project base addon conn-secret so they instead point at the env's own clone
// conn-secret. Used by AddEnvironment after the per-env addon clones are
// provisioned and envFromSecrets has been swapped (dropProjectAddonConns +
// append(clones)).
//
// Without this, a ${{ db.DATABASE_URL }} ref resolved against production lands
// in the service spec as an explicit env[].valueFrom.secretKeyRef{name:
// "<project>-db-conn"}. When that EnvVars list is adopted verbatim by a new
// staging env, the explicit secretKeyRef is copied unchanged — and an explicit
// env entry WINS over envFromSecrets on key collision in Kubernetes. So even
// though envFromSecrets correctly carries "<project>-db-staging-conn", the
// staging pod's DATABASE_URL still resolves to the PRODUCTION database. That
// silently defeats per-env isolation (staging writes hit prod).
//
// The base->clone mapping mirrors EnsureEnvAddons: a base conn named
// "<addon>-conn" maps to "<addon>-<envScope>-conn", and we only rewrite when
// the matching clone conn is actually present in the env's secret list.
func rescopeAddonConnRefs(in []kube.KusoEnvVar, droppedBaseConns, cloneConns []string, envScope string) []kube.KusoEnvVar {
	if envScope == "" || envScope == "production" || len(in) == 0 || len(droppedBaseConns) == 0 {
		return in
	}
	clonePresent := make(map[string]bool, len(cloneConns))
	for _, c := range cloneConns {
		clonePresent[c] = true
	}
	// Map each dropped base conn to its expected clone conn for this scope,
	// but only if that clone was actually provisioned for the env.
	baseToClone := make(map[string]string, len(droppedBaseConns))
	for _, base := range droppedBaseConns {
		short := strings.TrimSuffix(base, "-conn")
		clone := short + "-" + envScope + "-conn"
		if clonePresent[clone] {
			baseToClone[base] = clone
		}
	}
	if len(baseToClone) == 0 {
		return in
	}
	out := make([]kube.KusoEnvVar, len(in))
	for i, e := range in {
		out[i] = e
		if e.ValueFrom == nil {
			continue
		}
		skr, ok := e.ValueFrom["secretKeyRef"].(map[string]any)
		if !ok {
			continue
		}
		name, _ := skr["name"].(string)
		clone, hit := baseToClone[name]
		if !hit {
			continue
		}
		// Deep-copy the ValueFrom map so we don't mutate the source service
		// spec's slice (shared with the production env's EnvVars).
		newVF := make(map[string]any, len(e.ValueFrom))
		for k, v := range e.ValueFrom {
			newVF[k] = v
		}
		newSKR := make(map[string]any, len(skr))
		for k, v := range skr {
			newSKR[k] = v
		}
		newSKR["name"] = clone
		newVF["secretKeyRef"] = newSKR
		out[i].ValueFrom = newVF
	}
	return out
}

// dropProjectAddonConns removes the project's own addon conn-secrets from a
// list, leaving shared / instance / per-service / foo-conn secrets intact. Used
// when a per-env env swaps the shared project addons for its own per-env clones.
func dropProjectAddonConns(secrets, projectAddonConns []string) []string {
	if len(projectAddonConns) == 0 {
		return secrets
	}
	drop := make(map[string]bool, len(projectAddonConns))
	for _, n := range projectAddonConns {
		drop[n] = true
	}
	out := make([]string, 0, len(secrets))
	for _, sec := range secrets {
		if !drop[sec] {
			out = append(out, sec)
		}
	}
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
	// RMW via WithRetry: the in-process lockService above serializes
	// writers within one replica, but NOT across replicas. Fetch-mutate-
	// update under optimistic concurrency so a concurrent spec edit on
	// another pod (or a mid-flight operator status patch) can't be
	// clobbered — the mutation re-runs against the fresh object on 409.
	updated, err := s.Kube.UpdateKusoServiceWithRetry(ctx, ns, serviceCRName(project, service), func(svc *kube.KusoService) error {
		svc.Spec.SubscribedAddons = clean
		return nil
	})
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
