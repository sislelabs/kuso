package kube

import (
	"encoding/json"

	corev1 "k8s.io/api/core/v1"
)

// ToCoreEnvVar converts a KusoEnvVar into a typed corev1.EnvVar, preserving a
// `valueFrom` source (e.g. the secretKeyRef a `${{ addon.KEY }}` alias resolves
// to) via a JSON round-trip through the free-form ValueFrom map.
//
// Job builders (release hook, preview-migrate, `kuso run`) MUST use this rather
// than copying only Name+Value — otherwise a one-shot that reads an
// addon-aliased var (e.g. DATABASE_URI aliased from the addon's DATABASE_URL)
// gets an empty value and falls back to localhost. The Deployment renders the
// same env via Helm `toYaml`, so the long-running pods already get valueFrom;
// this brings Jobs to parity.
func (e KusoEnvVar) ToCoreEnvVar() corev1.EnvVar {
	ev := corev1.EnvVar{Name: e.Name, Value: e.Value}
	if src := envVarSourceFromMap(e.ValueFrom); src != nil {
		ev.ValueFrom = src
		ev.Value = "" // value and valueFrom are mutually exclusive in the kube API.
	}
	return ev
}

// CoreEnvVars maps a slice of KusoEnvVar to corev1.EnvVar, preserving valueFrom.
func CoreEnvVars(in []KusoEnvVar) []corev1.EnvVar {
	out := make([]corev1.EnvVar, 0, len(in))
	for _, e := range in {
		out = append(out, e.ToCoreEnvVar())
	}
	return out
}

// envVarSourceFromMap turns the preserve-unknown-fields ValueFrom map into a
// typed corev1.EnvVarSource. Returns nil for an empty/unparseable map, or one
// that carries no recognized source kind (so the caller keeps the plain Value
// rather than blanking the var).
func envVarSourceFromMap(m map[string]any) *corev1.EnvVarSource {
	if len(m) == 0 {
		return nil
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	var src corev1.EnvVarSource
	if err := json.Unmarshal(raw, &src); err != nil {
		return nil
	}
	if src.SecretKeyRef == nil && src.ConfigMapKeyRef == nil &&
		src.FieldRef == nil && src.ResourceFieldRef == nil {
		return nil
	}
	return &src
}
