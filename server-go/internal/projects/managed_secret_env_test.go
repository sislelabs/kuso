package projects

import (
	"testing"

	"kuso/server/internal/kube"
)

func TestMergeManagedSecretKeys(t *testing.T) {
	// spec.envVars already has a literal + a secretKeyRef against the
	// managed secret (DATABASE_URL-style). The managed secret also holds
	// two orphaned keys with no matching envVars entry.
	existing := []kube.KusoEnvVar{
		{Name: "NODE_ENV", Value: "production"},
		{Name: "INTERNAL_JWT_SECRET", ValueFrom: map[string]any{
			"secretKeyRef": map[string]any{"name": "svc-secrets", "key": "INTERNAL_JWT_SECRET"},
		}},
	}
	secretKeys := []string{"INTERNAL_JWT_SECRET", "WETRAVEL_API_KEY", "WETRAVEL_WEBHOOK_TOKEN"}

	got := mergeManagedSecretKeys(existing, "svc-secrets", secretKeys)

	byName := map[string]kube.KusoEnvVar{}
	for _, e := range got {
		byName[e.Name] = e
	}
	// The two orphaned keys are added, tagged managed-secret.
	for _, k := range []string{"WETRAVEL_API_KEY", "WETRAVEL_WEBHOOK_TOKEN"} {
		e, ok := byName[k]
		if !ok {
			t.Errorf("orphaned key %q not surfaced", k)
			continue
		}
		if e.Source != "managed-secret" {
			t.Errorf("%q source = %q, want managed-secret", k, e.Source)
		}
	}
	// INTERNAL_JWT_SECRET already had a secretKeyRef entry -> NOT duplicated.
	n := 0
	for _, e := range got {
		if e.Name == "INTERNAL_JWT_SECRET" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("INTERNAL_JWT_SECRET appears %d times, want 1 (no double-listing)", n)
	}
	// Existing entries preserved.
	if byName["NODE_ENV"].Value != "production" {
		t.Errorf("existing literal lost")
	}
	// Total = 2 existing + 2 newly surfaced.
	if len(got) != 4 {
		t.Errorf("got %d entries, want 4", len(got))
	}
}

func TestMergeManagedSecretKeys_NoSecret(t *testing.T) {
	existing := []kube.KusoEnvVar{{Name: "A", Value: "1"}}
	got := mergeManagedSecretKeys(existing, "svc-secrets", nil)
	if len(got) != 1 {
		t.Fatalf("no secret keys should leave envVars unchanged, got %d", len(got))
	}
}
