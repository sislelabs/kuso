package kube

import "testing"

// A `${{ db.DATABASE_URL }}` alias lands on the env CR as a KusoEnvVar whose
// ValueFrom map is {"secretKeyRef":{"name":"<proj>-db-conn","key":"DATABASE_URL"}}.
// Job builders (release/preview-migrate) previously copied only Name+Value,
// blanking these — so a release command reading DATABASE_URI connected to
// localhost. These tests pin that valueFrom now survives the conversion.
func TestToCoreEnvVar(t *testing.T) {
	t.Run("plain value", func(t *testing.T) {
		ev := KusoEnvVar{Name: "NODE_ENV", Value: "production"}.ToCoreEnvVar()
		if ev.Value != "production" || ev.ValueFrom != nil {
			t.Fatalf("plain value mangled: %+v", ev)
		}
	})

	t.Run("secretKeyRef valueFrom", func(t *testing.T) {
		ev := KusoEnvVar{
			Name: "DATABASE_URI",
			ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{
					"name": "scaffold-db-conn",
					"key":  "DATABASE_URL",
				},
			},
		}.ToCoreEnvVar()

		if ev.ValueFrom == nil || ev.ValueFrom.SecretKeyRef == nil {
			t.Fatalf("secretKeyRef dropped: %+v", ev)
		}
		if ev.ValueFrom.SecretKeyRef.Name != "scaffold-db-conn" {
			t.Errorf("secret name = %q, want scaffold-db-conn", ev.ValueFrom.SecretKeyRef.Name)
		}
		if ev.ValueFrom.SecretKeyRef.Key != "DATABASE_URL" {
			t.Errorf("secret key = %q, want DATABASE_URL", ev.ValueFrom.SecretKeyRef.Key)
		}
		if ev.Value != "" {
			t.Errorf("value must be empty when valueFrom is set, got %q", ev.Value)
		}
	})

	t.Run("empty valueFrom keeps plain value", func(t *testing.T) {
		ev := KusoEnvVar{Name: "X", Value: "y", ValueFrom: map[string]any{}}.ToCoreEnvVar()
		if ev.Value != "y" || ev.ValueFrom != nil {
			t.Fatalf("empty valueFrom should be a no-op: %+v", ev)
		}
	})

	t.Run("unrecognized valueFrom kind does not blank the var", func(t *testing.T) {
		ev := KusoEnvVar{Name: "X", Value: "y", ValueFrom: map[string]any{"bogusRef": map[string]any{"k": "v"}}}.ToCoreEnvVar()
		if ev.ValueFrom != nil {
			t.Fatalf("unrecognized source should yield nil ValueFrom: %+v", ev)
		}
		if ev.Value != "y" {
			t.Errorf("value should be preserved, got %q", ev.Value)
		}
	})
}

func TestCoreEnvVars(t *testing.T) {
	out := CoreEnvVars([]KusoEnvVar{
		{Name: "A", Value: "1"},
		{Name: "B", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "s", "key": "k"}}},
	})
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if out[0].Value != "1" {
		t.Errorf("A = %q", out[0].Value)
	}
	if out[1].ValueFrom == nil || out[1].ValueFrom.SecretKeyRef == nil {
		t.Errorf("B lost its secretKeyRef: %+v", out[1])
	}
}
