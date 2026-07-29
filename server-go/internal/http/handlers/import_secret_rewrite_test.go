package handlers

import (
	"testing"

	"kuso/server/internal/kube"
)

// TestRewriteSecretName covers the MED-5 rename rewrite: a secret name
// carrying the OLD project prefix maps to the new project.
func TestRewriteSecretName(t *testing.T) {
	t.Parallel()
	rm := map[string]string{"src": "dst"}
	cases := map[string]string{
		"src-pg-conn":       "dst-pg-conn",
		"src-shared":        "dst-shared",
		"src-web-secrets":   "dst-web-secrets",
		"src":               "dst",
		"other-pg-conn":     "other-pg-conn", // untouched
		"source-thing":      "source-thing",  // "src" is not a prefix of "source-"
	}
	for in, want := range cases {
		if got := rewriteSecretName(in, rm); got != want {
			t.Errorf("rewriteSecretName(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestRewriteEnvSecretRefs proves an env's EnvFromSecrets and
// valueFrom.secretKeyRef.name are both rewritten on rename — so an
// imported project reads its OWN secrets, not the source's (MED-5).
func TestRewriteEnvSecretRefs(t *testing.T) {
	t.Parallel()
	spec := &kube.KusoEnvironmentSpec{
		EnvFromSecrets: []string{"src-pg-conn", "src-shared", "keepme"},
		EnvVars: []kube.KusoEnvVar{
			{Name: "DB", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "src-pg-conn", "key": "url"},
			}},
			{Name: "PLAIN", Value: "x"},
		},
	}
	rewriteEnvSecretRefs(spec, map[string]string{"src": "dst"})

	if spec.EnvFromSecrets[0] != "dst-pg-conn" || spec.EnvFromSecrets[1] != "dst-shared" {
		t.Fatalf("EnvFromSecrets not rewritten: %v", spec.EnvFromSecrets)
	}
	if spec.EnvFromSecrets[2] != "keepme" {
		t.Fatalf("unrelated secret altered: %v", spec.EnvFromSecrets)
	}
	skr := spec.EnvVars[0].ValueFrom["secretKeyRef"].(map[string]any)
	if skr["name"] != "dst-pg-conn" {
		t.Fatalf("valueFrom.secretKeyRef.name not rewritten: %v", skr["name"])
	}
}
