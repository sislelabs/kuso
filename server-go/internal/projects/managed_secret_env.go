package projects

import (
	"context"

	"kuso/server/internal/kube"
)

// managedSecretSource is the Source tag on env vars enumerated from the
// kuso-managed <service>-secrets envFrom mount (keys that live in the
// secret but have no matching spec.envVars entry). The UI renders these
// as editable secret values.
const managedSecretSource = "managed-secret"

// mergeManagedSecretKeys returns envVars plus one entry per key in the
// kuso-managed secret `secretName` that is NOT already represented in
// envVars — either as a literal named the same, or as a secretKeyRef
// pointing at (secretName, key). The added entries are tagged
// Source=managed-secret with an empty Value (the caller fills/masks the
// value). Pure: does not read the cluster (secretKeys is supplied).
func mergeManagedSecretKeys(envVars []kube.KusoEnvVar, secretName string, secretKeys []string) []kube.KusoEnvVar {
	if len(secretKeys) == 0 {
		return envVars
	}
	// Names already represented by any spec.envVars entry, plus keys
	// already pulled in via a secretKeyRef against THIS secret.
	represented := make(map[string]bool, len(envVars))
	for _, e := range envVars {
		represented[e.Name] = true
		if skr := secretKeyRefOf(e); skr != nil && skr.name == secretName {
			represented[skr.key] = true
		}
	}
	out := envVars
	for _, k := range secretKeys {
		if represented[k] {
			continue
		}
		out = append(out, kube.KusoEnvVar{Name: k, Source: managedSecretSource})
	}
	return out
}

// EnrichServiceWithManagedSecretKeys merges keys from the service's
// kuso-managed <project>-<service>-secrets envFrom mount into the service
// CR's spec.EnvVars as managed-secret entries (masked value filled in by
// the handler). Best-effort: a missing secret or read error leaves the
// service untouched — surfacing is a convenience, not a correctness gate.
func (s *Service) EnrichServiceWithManagedSecretKeys(ctx context.Context, project, service string, svc *kube.KusoService) {
	if svc == nil || s.Kube == nil || s.Kube.Clientset == nil {
		return
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return
	}
	secretName := kube.ServiceSecretName(project, service)
	keys, err := s.listSecretKeys(ctx, ns, secretName)
	if err != nil {
		return
	}
	svc.Spec.EnvVars = mergeManagedSecretKeys(svc.Spec.EnvVars, secretName, keys)
}

// EnrichEnvWithManagedSecretKeys is the KusoEnvironment form. It reads BOTH
// the env-scoped secret (<project>-<service>-<env>-secrets) and the
// service-level secret (<project>-<service>-secrets) — an env pod envFrom-
// mounts whichever are in its envFromSecrets — and surfaces keys not already
// represented. Best-effort.
func (s *Service) EnrichEnvWithManagedSecretKeys(ctx context.Context, project string, env *kube.KusoEnvironment) {
	if env == nil || s.Kube == nil || s.Kube.Clientset == nil {
		return
	}
	ns, err := s.namespaceFor(ctx, project)
	if err != nil {
		return
	}
	svcShort := env.Spec.Service
	for _, secretName := range managedSecretNamesFor(env.Spec.EnvFromSecrets, project, svcShort, env.Spec.Kind, env.Name) {
		keys, err := s.listSecretKeys(ctx, ns, secretName)
		if err != nil {
			continue
		}
		env.Spec.EnvVars = mergeManagedSecretKeys(env.Spec.EnvVars, secretName, keys)
	}
}

// managedSecretNamesFor returns the kuso-managed service-secrets entries
// present in envFromSecrets (the <svc>-secrets / <svc>-<env>-secrets ones),
// NOT the <addon>-conn entries (addon-owned, already shown as secretKeyRefs).
func managedSecretNamesFor(envFromSecrets []string, project, svcShort, envKind, envName string) []string {
	want := map[string]bool{
		kube.ServiceSecretName(project, svcShort): true,
	}
	// Env-scoped secret name uses the env's short name; the env CR name is
	// <project>-<service>-<envshort>, so recompute against the trailing part.
	for _, s := range envFromSecrets {
		if want[s] {
			continue
		}
		// Accept any <project>-<svc>-*-secrets entry (env-scoped managed secret).
		if isManagedServiceSecretName(s, project, svcShort) {
			want[s] = true
		}
	}
	out := make([]string, 0, len(want))
	for _, s := range envFromSecrets {
		if want[s] {
			out = append(out, s)
		}
	}
	// The bare <svc>-secrets may not be in envFromSecrets but still exist;
	// include it so a service-level secret is always considered.
	base := kube.ServiceSecretName(project, svcShort)
	found := false
	for _, s := range out {
		if s == base {
			found = true
		}
	}
	if !found {
		out = append(out, base)
	}
	return out
}

// isManagedServiceSecretName reports whether name is a kuso-managed
// service-secrets secret for (project, svcShort): <project>-<svc>-secrets or
// <project>-<svc>-<env>-secrets. Excludes <addon>-conn and shared secrets.
func isManagedServiceSecretName(name, project, svcShort string) bool {
	prefix := project + "-" + svcShort + "-"
	return len(name) > len(prefix) &&
		name[:len(prefix)] == prefix &&
		len(name) >= len("-secrets") &&
		name[len(name)-len("-secrets"):] == "-secrets"
}

type secretKeyRefParts struct{ name, key string }

// secretKeyRefOf extracts the secretKeyRef {name,key} from an env var's
// free-form ValueFrom, or nil if it isn't a secretKeyRef.
func secretKeyRefOf(e kube.KusoEnvVar) *secretKeyRefParts {
	if e.ValueFrom == nil {
		return nil
	}
	raw, ok := e.ValueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		return nil
	}
	name, _ := raw["name"].(string)
	key, _ := raw["key"].(string)
	if name == "" || key == "" {
		return nil
	}
	return &secretKeyRefParts{name: name, key: key}
}
