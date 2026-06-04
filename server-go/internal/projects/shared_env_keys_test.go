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
			name: "env has shadow overrides + per-env-only entries + mirrors",
			svc: []kube.KusoEnvVar{
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.tickero.bg"},
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},
				{Name: "MIRROR_ME", Value: "same-everywhere"},
			},
			env: []kube.KusoEnvVar{
				// SHADOW OVERRIDE: same name as svc, different value.
				// Must survive (user staged this for staging).
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.tickero.bg"},
				// SHADOW OVERRIDE.
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "staging"},
				// MIRROR: identical to svc value — drop, propagation
				// will re-stamp from svc anyway.
				{Name: "MIRROR_ME", Value: "same-everywhere"},
				// PER-ENV-ONLY: net-new on this env.
				{Name: "NEXT_PUBLIC_SITE_URL", Value: "https://staging.tickero.bg"},
				{Name: "API_URL", Value: "http://tickero-api-staging.kuso.svc.cluster.local"},
			},
			wantNames: []string{
				"NEXT_PUBLIC_API_URL", "NEXT_PUBLIC_ENVIRONMENT",
				"NEXT_PUBLIC_SITE_URL", "API_URL",
			},
			wantValues: map[string]string{
				"NEXT_PUBLIC_API_URL":     "https://api-staging.tickero.bg",
				"NEXT_PUBLIC_ENVIRONMENT": "staging",
				"NEXT_PUBLIC_SITE_URL":    "https://staging.tickero.bg",
				"API_URL":                 "http://tickero-api-staging.kuso.svc.cluster.local",
			},
		},
		{
			name: "no env entries",
			svc:  []kube.KusoEnvVar{{Name: "FOO", Value: "bar"}},
			env:  nil,
		},
		{
			// REGRESSION (Coolify migration): an env entry that is an
			// unresolved ${{ ref }} literal is a STALE SEED, never a
			// deliberate per-env override. It must NOT shadow the
			// service's resolved value (a secretKeyRef or concrete
			// string) — otherwise the pod gets the literal "${{...}}"
			// and crashes (Prisma "scheme not recognized"). Drop it so
			// the service's resolved value propagates.
			name: "env has stale unresolved ${{ }} literal — not an override",
			svc: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": "app-db-conn", "key": "DATABASE_URL"},
				}},
			},
			env: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", Value: "${{ db.DATABASE_URL }}"},
			},
			wantNames: nil, // no override — service's secretKeyRef wins
		},
		{
			name: "env mirrors service exactly — all entries drop",
			svc: []kube.KusoEnvVar{
				{Name: "FOO", Value: "shared-value"},
				{Name: "BAR", Value: "shared-value"},
			},
			env: []kube.KusoEnvVar{
				{Name: "FOO", Value: "shared-value"},
				{Name: "BAR", Value: "shared-value"},
			},
			wantNames: nil,
		},
		{
			name: "all env entries shadow service with different values — all survive",
			svc: []kube.KusoEnvVar{
				{Name: "FOO", Value: "svc"},
				{Name: "BAR", Value: "svc"},
			},
			env: []kube.KusoEnvVar{
				{Name: "FOO", Value: "env-staged"},
				{Name: "BAR", Value: "env-staged"},
			},
			wantNames:  []string{"FOO", "BAR"},
			wantValues: map[string]string{"FOO": "env-staged", "BAR": "env-staged"},
		},
		{
			// B2.5 regression: a literal-value env override for a key
			// that's subscribed via sharedEnvKeys (not present on the
			// svc.spec.envVars list) is the canonical "I want to
			// override the shared default per-env without creating a
			// per-env Secret" case. extractEnvOnlyOverrides MUST treat
			// it as a net-new override so mergeExplicitOverrides can
			// later strip the subscribed valueFrom in favour of this
			// literal — otherwise the pod sees the shared default and
			// the user's edit silently disappears.
			name: "literal env override for subscribed-only key",
			svc:  nil, // svc.spec.envVars empty; key lives in sharedEnvKeys
			env: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "https://api-staging.tickero.bg"},
			},
			wantNames:  []string{"API_URL"},
			wantValues: map[string]string{"API_URL": "https://api-staging.tickero.bg"},
		},
		{
			// B2.5 regression (mirror case): an env-level entry that
			// re-stamps the subscribed valueFrom (legacy state from
			// propagation when valueFrom was written to env.spec.envVars
			// directly) must also survive as "net-new" — the dedupe in
			// mergeSubscribedEnvVars/mergeExplicitOverrides handles the
			// duplicate downstream.
			name: "valueFrom env override for subscribed-only key (legacy stamp)",
			svc:  nil,
			env: []kube.KusoEnvVar{
				{
					Name: "API_URL",
					ValueFrom: map[string]any{
						"secretKeyRef": map[string]any{
							"name": "myproject-shared",
							"key":  "API_URL",
						},
					},
				},
			},
			wantNames: []string{"API_URL"},
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
