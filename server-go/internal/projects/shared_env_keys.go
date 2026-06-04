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
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"

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
	envOverrideNames []string,
) (mergedEnvVars []kube.KusoEnvVar, prunedEnvFromSecrets []string, err error) {
	subscribed, missing, err := s.expandSharedEnvKeysToEnvVars(ctx, ns, project, sharedEnvKeys)
	if err != nil {
		return nil, nil, err
	}
	// B2.6: a subscribed key that no longer resolves to any shared
	// secret used to disappear silently — a user could delete an
	// upstream key and only notice when their pods 500'd. Log a WARN
	// here so it shows up in Loki/Grafana; the dashboard surfaces the
	// missing list via the env-detail GET (handler/services_env.go
	// returns spec.missingSharedKeys to the canvas which renders a
	// yellow chip). Pre-fix: the missing slice was thrown away with
	// `_,` so neither logs nor UI knew.
	if len(missing) > 0 {
		slog.WarnContext(ctx, "resolveSharedEnvKeysForEnv: subscribed keys not found in any shared secret",
			"project", project,
			"missing_keys", missing,
			"requested_count", len(sharedEnvKeys),
		)
	}
	// Drop subscribed-key envVars whose KEY is already overridden by
	// a per-env / per-service Secret in envFromSecrets. Explicit `env`
	// entries (which is what subscribed valueFrom produces) ALWAYS win
	// over `envFrom` in k8s, so leaving the shared-secret valueFrom
	// here would mask the per-env override and the pod would see
	// production values on staging. Looking ahead in envFromSecrets:
	// any non-shared secret whose key set includes one of our
	// subscribed names = "user has a per-env override; let envFrom
	// deliver it, drop this entry from envVars".
	overrideKeys := s.collectEnvFromOverrideKeys(ctx, ns, project, envFromSecrets)
	if len(overrideKeys) > 0 {
		filteredSubscribed := subscribed[:0]
		for _, e := range subscribed {
			if !overrideKeys[e.Name] {
				filteredSubscribed = append(filteredSubscribed, e)
			}
		}
		subscribed = filteredSubscribed
	}
	// Identify env-level overrides — env envVar entries whose name
	// is NOT on the service. Those are the user's per-env explicit
	// edits and must survive propagation, otherwise re-saving any
	// service-level field (placement, port, sharedEnvKeys, …)
	// silently flattens them back to service defaults.
	//
	// Crucially: drop any env entry that's a valueFrom.secretKeyRef
	// pointing at a project-shared/instance-shared secret AND whose
	// key is no longer in the current sharedEnvKeys subscription.
	// Those are leftover stamps from a previous propagation when the
	// subscription was wider; without this filter, unsubscribing
	// from a key on the service spec leaves the env still mounting
	// it (because the old valueFrom looks like a "user override" to
	// the diff against svcExplicitEnvVars, which never had the key).
	sharedSet := make(map[string]bool, len(sharedEnvKeys))
	for _, k := range sharedEnvKeys {
		sharedSet[k] = true
	}
	sharedSecretNames := make(map[string]bool, 2)
	for _, n := range kube.SharedSecretNames(project) {
		sharedSecretNames[n] = true
	}
	filteredEnvExplicit := make([]kube.KusoEnvVar, 0, len(envExplicitEnvVars))
	for _, e := range envExplicitEnvVars {
		if isUnsubscribedSharedSecretRef(e, sharedSet, sharedSecretNames) {
			continue
		}
		// Also drop shared-secret refs whose KEY has a per-env Secret
		// override. Otherwise the leftover valueFrom-to-tickero-shared
		// gets re-applied as a "net-new env override" by
		// extractEnvOnlyOverrides — which puts the production value
		// back even though the user set a per-env staging value.
		if isSharedSecretRefWithOverride(e, sharedSecretNames, overrideKeys) {
			continue
		}
		filteredEnvExplicit = append(filteredEnvExplicit, e)
	}
	overrideSet := make(map[string]bool, len(envOverrideNames))
	for _, n := range envOverrideNames {
		overrideSet[n] = true
	}
	envOverrides := extractEnvOnlyOverrides(svcExplicitEnvVars, filteredEnvExplicit, overrideSet)
	// Build merged envVars: subscribed (base) ← svc-explicit ← env-overrides.
	merged := mergeSubscribedEnvVars(svcExplicitEnvVars, subscribed)
	merged = mergeExplicitOverrides(merged, envOverrides)
	pruned := pruneSharedSecretsFromEnvFrom(project, envFromSecrets)
	_ = corev1.SecretTypeOpaque // silence unused import
	return merged, pruned, nil
}

// collectEnvFromOverrideKeys returns the set of keys present in any
// envFromSecrets entry that's NOT a project-shared / instance-shared
// secret. These are per-service / per-env Secrets the user has set
// up explicitly; their keys must override any subscribed shared-secret
// valueFrom (which would otherwise win because explicit env: beats
// envFrom: in k8s).
//
// Errors are swallowed: missing secrets contribute zero keys, which
// preserves the prior behavior. A real secret-read failure would
// also fall through; in that case the worst case is the override
// gets masked, which the user can recover from by re-saving.
func (s *Service) collectEnvFromOverrideKeys(ctx context.Context, ns, project string, envFromSecrets []string) map[string]bool {
	if len(envFromSecrets) == 0 {
		return nil
	}
	sharedNames := map[string]bool{}
	for _, n := range kube.SharedSecretNames(project) {
		sharedNames[n] = true
	}
	out := map[string]bool{}
	for _, name := range envFromSecrets {
		if sharedNames[name] {
			continue
		}
		// Skip addon conn-secrets too — they belong to addons, not
		// user overrides, and their keys (DATABASE_URL etc) shouldn't
		// shadow a user's explicit shared subscription.
		if strings.HasSuffix(name, "-conn") {
			continue
		}
		sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			continue
		}
		for k := range sec.Data {
			out[k] = true
		}
	}
	return out
}

// isSharedSecretRefWithOverride returns true when an env's envVar
// entry is a valueFrom.secretKeyRef into a project-shared/instance-
// shared secret AND the key is also present in a per-env Secret
// (overrideKeys). The shared-secret ref must be dropped so the
// per-env Secret's value can reach the pod via envFrom (explicit
// env: would otherwise win over envFrom:).
func isSharedSecretRefWithOverride(e kube.KusoEnvVar, sharedSecretNames, overrideKeys map[string]bool) bool {
	if e.ValueFrom == nil || !overrideKeys[e.Name] {
		return false
	}
	refRaw, ok := e.ValueFrom["secretKeyRef"]
	if !ok {
		return false
	}
	refMap, ok := refRaw.(map[string]any)
	if !ok {
		return false
	}
	name, _ := refMap["name"].(string)
	return sharedSecretNames[name]
}

// isUnsubscribedSharedSecretRef returns true when an env's envVar
// entry is a valueFrom.secretKeyRef into a project-shared/instance-
// shared secret AND that key is no longer on the active subscription.
// Those entries are propagation leftovers that should be dropped so
// the pod stops seeing the key after an unsubscribe.
//
// Plain `value` entries (no valueFrom) are never matched — those are
// genuine user overrides regardless of name overlap with shared keys.
func isUnsubscribedSharedSecretRef(e kube.KusoEnvVar, sharedSet, sharedSecretNames map[string]bool) bool {
	if e.ValueFrom == nil {
		return false
	}
	refRaw, ok := e.ValueFrom["secretKeyRef"]
	if !ok {
		return false
	}
	refMap, ok := refRaw.(map[string]any)
	if !ok {
		return false
	}
	name, _ := refMap["name"].(string)
	if !sharedSecretNames[name] {
		return false
	}
	return !sharedSet[e.Name]
}

// extractEnvOnlyOverrides returns env envVar entries that should
// survive propagation as per-env overrides. Two flavours qualify:
//
//  1. **Net-new on this env** — name doesn't exist on the service at
//     all. These are unambiguously per-env (e.g. NEXT_PUBLIC_SITE_URL
//     defined only on the staging env, or a subscribed-key override
//     that lives in sharedEnvKeys rather than svc.spec.envVars).
//
//  2. **Marked shadow overrides** — name exists on the service AND the
//     name is in overrideNames, the EXPLICIT set of vars the user
//     deliberately pinned to this env via the per-env scoped editor
//     (env.Spec.EnvOverrides). The user set this env's value on purpose
//     (staging overrides production's NEXT_PUBLIC_API_URL); it must NOT
//     get re-stamped to the service default.
//
// Everything else — a same-name entry NOT in overrideNames — drops,
// regardless of whether its value matches the service. This is the
// crux of the fix: we no longer GUESS "override" from value-difference.
// A drifted inherited seed (e.g. the production env seeded with
// AUTH_URL=ticketmaster at AddService, then the service value changed
// to web.jira-mudira) differs from the service but is NOT a deliberate
// override — value-comparison wrongly kept it and permanently shadowed
// the service, so a service-level edit could never reach the env. With
// the explicit marker, only genuinely-pinned vars survive; drifted
// seeds drop and get re-stamped from the (newer) service value.
//
// overrideNames may be nil (no deliberate overrides recorded — the
// common case for auto-seeded production envs and for every env
// migrated before this field existed). A nil set means "trust the
// service": all same-name entries drop and re-stamp. Net-new entries
// still survive because they have no service value to fall back to.
func extractEnvOnlyOverrides(svcExplicit, envExplicit []kube.KusoEnvVar, overrideNames map[string]bool) []kube.KusoEnvVar {
	if len(envExplicit) == 0 {
		return nil
	}
	svcByName := make(map[string]kube.KusoEnvVar, len(svcExplicit))
	for _, e := range svcExplicit {
		svcByName[e.Name] = e
	}
	out := make([]kube.KusoEnvVar, 0, len(envExplicit))
	for _, e := range envExplicit {
		// An env entry holding an UNRESOLVED `${{ ref }}` literal is a
		// stale seed (written before the ref could resolve — e.g. the
		// auto-created production env seeded with `${{ db.DATABASE_URL }}`
		// before the addon's conn Secret existed), NOT a deliberate
		// per-env override. Treating it as an override re-stamps the raw
		// literal over the service's RESOLVED value (secretKeyRef /
		// concrete string), and the pod gets "${{...}}" verbatim and
		// crashes. Skip it (even if somehow marked) so the service's
		// resolved value propagates. A genuine override is always a
		// concrete value or a resolved ref, never an unresolved `${{ }}`.
		if _, isRef, _ := ParseVarRef(e.Value); isRef {
			continue
		}
		if _, exists := svcByName[e.Name]; !exists {
			// Net-new: no service value to fall back to, so it can only
			// live on the env. Always survives.
			out = append(out, e)
			continue
		}
		// Same name as the service: survives ONLY if the user explicitly
		// pinned it on this env. Unmarked same-name entries are inherited
		// seeds (possibly drifted) — drop them so the service value wins.
		if overrideNames[e.Name] {
			out = append(out, e)
		}
	}
	return out
}

// envVarsEqual reports whether two KusoEnvVar entries carry the same
// payload (name + value + valueFrom shape). Used by extractEnv
// OnlyOverrides to tell mirrors from shadow overrides. Comparison
// is conservative: anything not byte-identical is considered an
// override (a user who reformats valueFrom shouldn't lose their
// edit either).
func envVarsEqual(a, b kube.KusoEnvVar) bool {
	if a.Name != b.Name || a.Value != b.Value {
		return false
	}
	// ValueFrom is a free-form map (we don't lose secretKeyRef on
	// round-trip). Compare via reflect-style key set equality —
	// can't reach in for typed fields without an import dance.
	if len(a.ValueFrom) != len(b.ValueFrom) {
		return false
	}
	for k, av := range a.ValueFrom {
		bv, ok := b.ValueFrom[k]
		if !ok {
			return false
		}
		// ValueFrom values are themselves nested maps (secretKeyRef
		// is {name, key}). JSON-roundtrip equality is the simplest
		// safe comparison.
		if !deepEqualJSON(av, bv) {
			return false
		}
	}
	return true
}

// deepEqualJSON compares two values via JSON marshal — handles nested
// maps without pulling in reflect.DeepEqual surprises (NaN, function
// values). The inputs come from JSON parsing originally so this
// round-trip is loss-free.
func deepEqualJSON(a, b any) bool {
	ja, err := json.Marshal(a)
	if err != nil {
		return false
	}
	jb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return string(ja) == string(jb)
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
