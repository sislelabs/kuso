// Package projects: per-service subscription of project + instance
// shared-secret keys. See KusoService.spec.sharedEnvKeys.
//
// Design (v0.16.10):
//   - nil sharedEnvKeys = legacy behavior: chart blanket-mounts every
//     project + instance shared secret via envFromSecrets (unchanged).
//   - non-nil sharedEnvKeys = explicit subscription: the kuso server
//     resolves each key to its source secret + emits an env entry
//     (name + valueFrom.secretKeyRef) onto the env CR's envVars.
//     The chart's existing envVars renderer mounts them per-key, so
//     keys NOT in the subscription list don't reach the pod.
//
// The two well-known shared-secret names live on `kube.SharedSecretNames`
// (project-shared first, instance-shared second). When the same key
// exists in both, project-shared wins — same precedence as today's
// envFromSecrets ordering.

package projects

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// expandSharedEnvKeysToEnvVars resolves each requested key to its
// source secret and returns env entries (valueFrom.secretKeyRef
// shape). Keys that exist in neither shared secret are silently
// dropped — the chart renders the env list as-is, and a non-existent
// key would crash the pod with "couldn't find key X in Secret Y."
// Better to skip with a warning than crashloop a deploy.
//
// Returns the resolved entries + the keys that could not be resolved
// (the latter is used to surface a UI warning, not to fail the call).
func (s *Service) expandSharedEnvKeysToEnvVars(
	ctx context.Context,
	ns, project string,
	requestedKeys []string,
) (resolved []kube.KusoEnvVar, missing []string, err error) {
	if len(requestedKeys) == 0 {
		return nil, nil, nil
	}

	// Pull both well-known shared secrets. Either may be absent — a
	// fresh project has no <project>-shared yet; an admin who never
	// wrote instance secrets has no kuso-instance-shared. Both
	// missing = nothing to resolve.
	secretNames := kube.SharedSecretNames(project)
	keysBySecret := make(map[string]map[string]bool, len(secretNames))
	for _, name := range secretNames {
		sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, nil, fmt.Errorf("get secret %s/%s: %w", ns, name, err)
		}
		ks := make(map[string]bool, len(sec.Data))
		for k := range sec.Data {
			ks[k] = true
		}
		keysBySecret[name] = ks
	}

	// Resolve in the project-first, instance-second order so a key
	// present in both falls to project-shared. Matches legacy
	// envFromSecrets precedence (later entries win, and project comes
	// before instance in the envFrom list — same outcome here by
	// iterating secretNames in order and stopping at the first hit).
	resolved = make([]kube.KusoEnvVar, 0, len(requestedKeys))
	for _, key := range requestedKeys {
		var sourceSecret string
		for _, name := range secretNames {
			if keysBySecret[name][key] {
				sourceSecret = name
				break
			}
		}
		if sourceSecret == "" {
			missing = append(missing, key)
			continue
		}
		resolved = append(resolved, kube.KusoEnvVar{
			Name: key,
			// ValueFrom is a map[string]any on the kube type so we
			// don't have to maintain a parallel typed schema for
			// every kube valueFrom shape. The downstream chart
			// renderer copies this verbatim into the pod's env list.
			ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{
					"name": sourceSecret,
					"key":  key,
				},
			},
		})
	}
	// Stable sort by name so repeat reconciles produce identical
	// specs and the env CR doesn't appear "dirty" to the operator.
	sort.Slice(resolved, func(i, j int) bool {
		return resolved[i].Name < resolved[j].Name
	})
	return resolved, missing, nil
}

// mergeSubscribedEnvVars returns the union of explicit envVars (from
// the user) + subscribed shared-secret envVars, with explicit entries
// winning on name collision (a user who set both an explicit DATABASE_URL
// override and subscribed to DATABASE_URL in the shared secret gets
// their explicit one).
func mergeSubscribedEnvVars(explicit, subscribed []kube.KusoEnvVar) []kube.KusoEnvVar {
	if len(subscribed) == 0 {
		return explicit
	}
	seen := make(map[string]bool, len(explicit))
	for _, e := range explicit {
		seen[e.Name] = true
	}
	out := make([]kube.KusoEnvVar, 0, len(explicit)+len(subscribed))
	out = append(out, explicit...)
	for _, e := range subscribed {
		if !seen[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

// pruneSharedSecretsFromEnvFrom strips project + instance shared
// secret names from envFromSecrets. Addon conn-secrets stay because
// they're still blanket-mounted as part of the addon contract.
func pruneSharedSecretsFromEnvFrom(project string, envFromSecrets []string) []string {
	stripSet := make(map[string]bool, 2)
	for _, n := range kube.SharedSecretNames(project) {
		stripSet[n] = true
	}
	out := make([]string, 0, len(envFromSecrets))
	for _, s := range envFromSecrets {
		if !stripSet[s] {
			out = append(out, s)
		}
	}
	return out
}

// resolveSharedEnvKeysForEnv computes the merged envVars + filtered
// envFromSecrets for a single environment. Subscribed keys become
// explicit valueFrom entries on envVars; the two well-known shared
// secret names are stripped from envFromSecrets so adding a new key
// to a shared secret doesn't silently leak into unsubscribed services.
//
// Precedence on name collision (last wins): subscribed shared-secret
// keys < service-level explicit envVars < env-level explicit overrides.
// A per-env NEXT_PUBLIC_API_URL=api-staging.tickero.bg wins over the
// service-level NEXT_PUBLIC_API_URL=api.tickero.bg every time.
//
// Pass envExplicitEnvVars = nil for the AddService / first-env path
// where there's no existing env yet to preserve.
func (s *Service) resolveSharedEnvKeysForEnv(
	ctx context.Context,
	ns, project string,
	sharedEnvKeys []string,
	svcExplicitEnvVars []kube.KusoEnvVar,
	envExplicitEnvVars []kube.KusoEnvVar,
	envFromSecrets []string,
) (mergedEnvVars []kube.KusoEnvVar, prunedEnvFromSecrets []string, err error) {
	subscribed, _, err := s.expandSharedEnvKeysToEnvVars(ctx, ns, project, sharedEnvKeys)
	if err != nil {
		return nil, nil, err
	}
	// Identify env-level overrides — env envVar entries whose name
	// is NOT on the service. Those are the user's per-env explicit
	// edits and must survive propagation, otherwise re-saving any
	// service-level field (placement, port, sharedEnvKeys, …)
	// silently flattens them back to service defaults.
	envOverrides := extractEnvOnlyOverrides(svcExplicitEnvVars, envExplicitEnvVars)
	// Build merged envVars: subscribed (base) ← svc-explicit ← env-overrides.
	merged := mergeSubscribedEnvVars(svcExplicitEnvVars, subscribed)
	merged = mergeExplicitOverrides(merged, envOverrides)
	pruned := pruneSharedSecretsFromEnvFrom(project, envFromSecrets)
	_ = corev1.SecretTypeOpaque // silence unused import
	return merged, pruned, nil
}

// extractEnvOnlyOverrides returns the env's envVar entries whose names
// don't exist on the parent service. Those entries are the per-env
// overrides the user set explicitly through the dashboard's "edit on
// this env only" path; they must survive any service-level write.
//
// Service-level envVar with the same name = the env is just mirroring
// the service; nothing to preserve since propagation will re-stamp
// the (possibly newer) service value anyway.
func extractEnvOnlyOverrides(svcExplicit, envExplicit []kube.KusoEnvVar) []kube.KusoEnvVar {
	if len(envExplicit) == 0 {
		return nil
	}
	svcNames := make(map[string]bool, len(svcExplicit))
	for _, e := range svcExplicit {
		svcNames[e.Name] = true
	}
	out := make([]kube.KusoEnvVar, 0, len(envExplicit))
	for _, e := range envExplicit {
		if !svcNames[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

// mergeExplicitOverrides layers env-level overrides on top of an
// already-merged envVars list. On name collision the override wins.
// Used so per-env edits beat service-level + subscribed defaults.
func mergeExplicitOverrides(base, overrides []kube.KusoEnvVar) []kube.KusoEnvVar {
	if len(overrides) == 0 {
		return base
	}
	overrideNames := make(map[string]bool, len(overrides))
	for _, e := range overrides {
		overrideNames[e.Name] = true
	}
	out := make([]kube.KusoEnvVar, 0, len(base)+len(overrides))
	for _, e := range base {
		if !overrideNames[e.Name] {
			out = append(out, e)
		}
	}
	out = append(out, overrides...)
	return out
}
