package runs

import (
	"testing"

	"kuso/server/internal/kube"
)

func names(vs []kube.KusoRunEnv) map[string]string {
	m := make(map[string]string, len(vs))
	for _, v := range vs {
		m[v.Name] = v.Value
	}
	return m
}

func TestMergeRunEnv_IncludesServicePlainVars(t *testing.T) {
	svc := []kube.KusoEnvVar{
		{Name: "INTERNAL_SYSTEM_URL", Value: "https://internal.example.com"},
		{Name: "NODE_ENV", Value: "production"},
	}
	got := mergeRunEnv(svc, nil)
	m := names(got)
	if m["INTERNAL_SYSTEM_URL"] != "https://internal.example.com" {
		t.Fatalf("service plain var not carried into run env: %#v", m)
	}
	if m["NODE_ENV"] != "production" {
		t.Fatalf("expected NODE_ENV=production, got %q", m["NODE_ENV"])
	}
}

func TestMergeRunEnv_SkipsValueFromVars(t *testing.T) {
	svc := []kube.KusoEnvVar{
		{Name: "DATABASE_URL", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "db-conn", "key": "DATABASE_URL"}}},
		{Name: "PLAIN", Value: "ok"},
	}
	got := mergeRunEnv(svc, nil)
	m := names(got)
	if _, ok := m["DATABASE_URL"]; ok {
		t.Fatalf("valueFrom var must be skipped (it arrives via envFromSecrets): %#v", m)
	}
	if m["PLAIN"] != "ok" {
		t.Fatalf("plain var should remain: %#v", m)
	}
}

func TestMergeRunEnv_OverlayWinsAndAdds(t *testing.T) {
	svc := []kube.KusoEnvVar{{Name: "MIGRATE", Value: "false"}, {Name: "KEEP", Value: "1"}}
	overlay := []EnvVar{{Name: "MIGRATE", Value: "true"}, {Name: "EXTRA", Value: "x"}}
	got := mergeRunEnv(svc, overlay)
	m := names(got)
	if m["MIGRATE"] != "true" {
		t.Fatalf("overlay should override service value: MIGRATE=%q", m["MIGRATE"])
	}
	if m["KEEP"] != "1" {
		t.Fatalf("untouched service var lost: %#v", m)
	}
	if m["EXTRA"] != "x" {
		t.Fatalf("overlay-only var not added: %#v", m)
	}
	// no duplicate MIGRATE entries
	count := 0
	for _, v := range got {
		if v.Name == "MIGRATE" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one MIGRATE entry, got %d", count)
	}
}

func TestMergeRunEnv_SkipsEmptyNames(t *testing.T) {
	got := mergeRunEnv([]kube.KusoEnvVar{{Name: "", Value: "nope"}}, []EnvVar{{Name: "", Value: "nope2"}})
	if len(got) != 0 {
		t.Fatalf("empty-named vars must be skipped, got %#v", got)
	}
}
