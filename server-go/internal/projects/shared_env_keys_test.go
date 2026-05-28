package projects

import (
	"testing"

	"kuso/server/internal/kube"
)

// TestExtractEnvOnlyOverrides locks in the rule for distinguishing
// per-env overrides (names not on the service) from env entries that
// merely mirror the service. Only the env-only entries survive
// propagation; service-mirrored entries get re-stamped from the
// service spec anyway.
func TestExtractEnvOnlyOverrides(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		svc         []kube.KusoEnvVar
		env         []kube.KusoEnvVar
		wantNames   []string
		wantValues  map[string]string
	}{
		{
			name: "env has overrides + mirrors",
			svc: []kube.KusoEnvVar{
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.tickero.bg"},
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},
			},
			env: []kube.KusoEnvVar{
				// per-env override of a service var — must NOT be
				// counted as env-only (svc has it). It'll be re-stamped
				// from svc, which is the intended behaviour for a
				// service-managed var.
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.tickero.bg"},
				// per-env-only var — must survive
				{Name: "NEXT_PUBLIC_SITE_URL", Value: "https://staging.tickero.bg"},
				// per-env-only var — must survive
				{Name: "API_URL", Value: "http://tickero-api-staging.kuso.svc.cluster.local"},
			},
			wantNames:  []string{"NEXT_PUBLIC_SITE_URL", "API_URL"},
			wantValues: map[string]string{"NEXT_PUBLIC_SITE_URL": "https://staging.tickero.bg", "API_URL": "http://tickero-api-staging.kuso.svc.cluster.local"},
		},
		{
			name: "no env entries",
			svc:  []kube.KusoEnvVar{{Name: "FOO", Value: "bar"}},
			env:  nil,
		},
		{
			name: "all env entries shadowed by service",
			svc: []kube.KusoEnvVar{
				{Name: "FOO", Value: "svc"},
				{Name: "BAR", Value: "svc"},
			},
			env: []kube.KusoEnvVar{
				{Name: "FOO", Value: "env"},
				{Name: "BAR", Value: "env"},
			},
			wantNames: nil,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractEnvOnlyOverrides(tc.svc, tc.env)
			if len(got) != len(tc.wantNames) {
				t.Fatalf("got %d overrides, want %d (%v)", len(got), len(tc.wantNames), got)
			}
			for i, e := range got {
				if e.Name != tc.wantNames[i] {
					t.Errorf("idx %d: name = %q, want %q", i, e.Name, tc.wantNames[i])
				}
				if want, ok := tc.wantValues[e.Name]; ok && e.Value != want {
					t.Errorf("name %s: value = %q, want %q", e.Name, e.Value, want)
				}
			}
		})
	}
}

// TestMergeExplicitOverrides locks in the "env-level overrides
// win on name collision" rule. This is the path that prevents
// propagation from silently flattening a staging env's per-env
// envVars back to service defaults every time someone clicks an
// unrelated chip.
func TestMergeExplicitOverrides(t *testing.T) {
	t.Parallel()

	base := []kube.KusoEnvVar{
		{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.tickero.bg"},   // svc-level default
		{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},           // svc-level default
		{Name: "DATABASE_URL", Value: "subscribed"},                       // subscribed
	}
	overrides := []kube.KusoEnvVar{
		{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.tickero.bg"}, // per-env override
		{Name: "NEXT_PUBLIC_SITE_URL", Value: "https://staging.tickero.bg"},     // per-env-only
	}
	got := mergeExplicitOverrides(base, overrides)

	// Map by name and assert the override won where set + base
	// survived where there was no override.
	byName := make(map[string]string, len(got))
	for _, e := range got {
		byName[e.Name] = e.Value
	}
	if v := byName["NEXT_PUBLIC_API_URL"]; v != "https://api-staging.tickero.bg" {
		t.Errorf("NEXT_PUBLIC_API_URL = %q, want staging override", v)
	}
	if v := byName["NEXT_PUBLIC_ENVIRONMENT"]; v != "production" {
		t.Errorf("NEXT_PUBLIC_ENVIRONMENT = %q, want production (svc default survived)", v)
	}
	if v := byName["DATABASE_URL"]; v != "subscribed" {
		t.Errorf("DATABASE_URL = %q, want subscribed (no override)", v)
	}
	if v := byName["NEXT_PUBLIC_SITE_URL"]; v != "https://staging.tickero.bg" {
		t.Errorf("NEXT_PUBLIC_SITE_URL = %q, want per-env-only", v)
	}
	if len(got) != 4 {
		t.Errorf("len = %d, want 4 (no duplicates)", len(got))
	}
}
