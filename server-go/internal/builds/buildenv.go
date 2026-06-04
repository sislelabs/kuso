package builds

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// reservedBuildEnvKeys are kuso/kubelet-managed names that must never be
// injected as build-time env — kuso owns them at runtime (PORT desyncs from
// spec.port; the rest are container bookkeeping). Mirrors the build job's own
// RESERVED list.
var reservedBuildEnvKeys = map[string]bool{
	"PORT": true, "HOSTNAME": true, "HOME": true, "PATH": true,
	"PWD": true, "USER": true, "SHELL": true, "TERM": true,
}

// secretLookup returns the literal value of a secret key, or false if absent.
type secretLookup func(secret, key string) (string, bool)

// buildEnvFromVars resolves a service's env vars into build-time KEY=VALUE
// literals. Literal Values pass through; secretKeyRef vars are resolved via
// lookup; unresolvable refs and reserved keys are omitted. Pure (lookup
// injected) so it's unit-testable without kube.
func buildEnvFromVars(vars []kube.KusoEnvVar, lookup secretLookup) map[string]string {
	out := map[string]string{}
	for _, v := range vars {
		if v.Name == "" || reservedBuildEnvKeys[v.Name] {
			continue
		}
		if v.Value != "" {
			out[v.Name] = v.Value
			continue
		}
		// secretKeyRef: valueFrom.secretKeyRef.{name,key}
		secret, key := secretKeyRefOf(v.ValueFrom)
		if secret == "" || key == "" {
			continue
		}
		if val, ok := lookup(secret, key); ok {
			out[v.Name] = val
		}
		// unresolvable → omit (addon conn secret may not exist yet)
	}
	return out
}

// secretKeyRefOf pulls {name,key} out of a valueFrom map's secretKeyRef.
func secretKeyRefOf(valueFrom map[string]any) (name, key string) {
	if valueFrom == nil {
		return "", ""
	}
	ref, ok := valueFrom["secretKeyRef"].(map[string]any)
	if !ok {
		return "", ""
	}
	name, _ = ref["name"].(string)
	key, _ = ref["key"].(string)
	return name, key
}

// resolveBuildEnv gathers a service's effective env as build-time literals,
// reading referenced secrets from the cluster. Used at build-trigger to
// populate KusoBuild.spec.buildEnv. A missing/unreadable secret just omits that
// var (logged by the caller) — never fatal.
func (s *Service) resolveBuildEnv(ctx context.Context, ns string, vars []kube.KusoEnvVar) map[string]string {
	cache := map[string]map[string][]byte{}
	lookup := func(secret, key string) (string, bool) {
		data, seen := cache[secret]
		if !seen {
			sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, secret, metav1.GetOptions{})
			if err != nil {
				cache[secret] = nil
				return "", false
			}
			data = sec.Data
			cache[secret] = data
		}
		if data == nil {
			return "", false
		}
		b, ok := data[key]
		return string(b), ok
	}
	return buildEnvFromVars(vars, lookup)
}
