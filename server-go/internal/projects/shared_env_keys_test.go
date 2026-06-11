package projects

import (
	"testing"

	"kuso/server/internal/kube"
)

// TestExtractEnvOnlyOverrides locks in the rule for distinguishing
// per-env overrides from env entries that merely mirror (or have
// drifted from) the service.
//
// The classification is driven by an EXPLICIT marker set
// (env.Spec.EnvOverrides — the names the user deliberately pinned via
// the per-env scoped editor), NOT by value-comparison against the
// service. Value-comparison is unsound: a stale inherited seed whose
// value has drifted from the service looks identical to a deliberate
// override, so a service-level edit could never reach the env (the
// jira-mudira "redirects to ticketmaster" bug). The marker removes the
// guessing: only names in the set (or net-new names absent from the
// service entirely) survive; everything else drops and gets re-stamped
// from the (newer) service value.
func TestExtractEnvOnlyOverrides(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		svc        []kube.KusoEnvVar
		env        []kube.KusoEnvVar
		overrides  map[string]bool // env.Spec.EnvOverrides as a set
		shared     map[string]bool // subscribed sharedEnvKeys as a set
		wantNames  []string
		wantValues map[string]string
	}{
		{
			name: "marked shadow overrides + net-new survive; mirrors + drifted-but-unmarked drop",
			svc: []kube.KusoEnvVar{
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.tickero.bg"},
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},
				{Name: "MIRROR_ME", Value: "same-everywhere"},
			},
			env: []kube.KusoEnvVar{
				// MARKED shadow override: user staged this for staging.
				{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.tickero.bg"},
				// MARKED shadow override.
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "staging"},
				// MIRROR: identical to svc — drop, re-stamped from svc.
				{Name: "MIRROR_ME", Value: "same-everywhere"},
				// PER-ENV-ONLY: net-new on this env, not on svc.
				{Name: "NEXT_PUBLIC_SITE_URL", Value: "https://staging.tickero.bg"},
				{Name: "API_URL", Value: "http://tickero-api-staging.kuso.svc.cluster.local"},
			},
			// NEXT_PUBLIC_SITE_URL + API_URL are subscribed shared keys with
			// per-env literal overrides (net-new vs svc.envVars) — survive.
			shared: map[string]bool{"NEXT_PUBLIC_SITE_URL": true, "API_URL": true},
			overrides: map[string]bool{
				"NEXT_PUBLIC_API_URL":     true,
				"NEXT_PUBLIC_ENVIRONMENT": true,
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
			// THE jira-mudira REGRESSION. The env carries a stale
			// inherited seed (AUTH_URL=ticketmaster.sisle.org) that has
			// drifted from the service value (web.jira-mudira...). It is
			// NOT in the override set — it's a leftover from AddService
			// seeding, never a deliberate per-env edit. It MUST drop so
			// the service's current value propagates. The old
			// value-comparison rule wrongly kept it (differs from svc =>
			// "override"), permanently shadowing the service and sending
			// users to the wrong host.
			name: "drifted seed not in override set — must drop (jira-mudira)",
			svc: []kube.KusoEnvVar{
				{Name: "AUTH_URL", Value: "https://web.jira-mudira.kuso.sislelabs.com"},
			},
			env: []kube.KusoEnvVar{
				{Name: "AUTH_URL", Value: "https://ticketmaster.sisle.org"},
			},
			overrides: nil, // no deliberate override recorded
			wantNames: nil,  // drops; service value wins
		},
		{
			name:      "no env entries",
			svc:       []kube.KusoEnvVar{{Name: "FOO", Value: "bar"}},
			env:       nil,
			overrides: nil,
		},
		{
			// An env entry holding an unresolved ${{ ref }} literal is a
			// stale seed (written before the ref could resolve), never a
			// deliberate override — drop it even if somehow marked, so the
			// service's resolved value (secretKeyRef / concrete) wins and
			// the pod never sees the literal "${{...}}".
			name: "env has stale unresolved ${{ }} literal — not an override",
			svc: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": "app-db-conn", "key": "DATABASE_URL"},
				}},
			},
			env: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", Value: "${{ db.DATABASE_URL }}"},
			},
			overrides: map[string]bool{"DATABASE_URL": true}, // even if marked, refs drop
			wantNames: nil,
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
			overrides: nil,
			wantNames: nil,
		},
		{
			name: "marked overrides survive even when same name as service",
			svc: []kube.KusoEnvVar{
				{Name: "FOO", Value: "svc"},
				{Name: "BAR", Value: "svc"},
			},
			env: []kube.KusoEnvVar{
				{Name: "FOO", Value: "env-staged"},
				{Name: "BAR", Value: "env-staged"},
			},
			overrides:  map[string]bool{"FOO": true, "BAR": true},
			wantNames:  []string{"FOO", "BAR"},
			wantValues: map[string]string{"FOO": "env-staged", "BAR": "env-staged"},
		},
		{
			// A literal-value env override for a key that's subscribed via
			// sharedEnvKeys (not on svc.spec.envVars) — the canonical
			// "override the shared default per-env" case. It's net-new
			// relative to the service envVars list, so it survives without
			// needing to be in the marker set (net-new is always kept).
			name:      "literal env override for subscribed-only key (net-new)",
			svc:       nil,
			env:       []kube.KusoEnvVar{{Name: "API_URL", Value: "https://api-staging.tickero.bg"}},
			overrides: nil,
			shared:    map[string]bool{"API_URL": true},
			wantNames: []string{"API_URL"},
			wantValues: map[string]string{
				"API_URL": "https://api-staging.tickero.bg",
			},
		},
		{
			// valueFrom env override for a subscribed-only key (legacy
			// stamp) — net-new relative to svc.envVars, survives.
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
			overrides: nil,
			shared:    map[string]bool{"API_URL": true},
			wantNames: []string{"API_URL"},
		},
		{
			// Regression: a var the SERVICE used to have and then `env unset`
			// removed. It's gone from svc.envVars but the env CR still carries
			// the old copy (propagation re-reads the env). It is NOT a marked
			// per-env override and NOT a subscribed shared key — so it must
			// DROP, otherwise `env unset` never reaches the pod. This is the
			// DATABASE_SSL_NO_VERIFY crash: unset on the service, but the env
			// kept forcing SSL against a plaintext Postgres.
			name:      "unset service var orphaned on env — must drop",
			svc:       nil, // service no longer has it (unset)
			env:       []kube.KusoEnvVar{{Name: "DATABASE_SSL_NO_VERIFY", Value: "true"}},
			overrides: nil, // never a deliberate per-env override
			shared:    nil, // not a subscribed shared key
			wantNames: nil, // drops
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractEnvOnlyOverrides(tc.svc, tc.env, tc.overrides, tc.shared)
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
		{Name: "NEXT_PUBLIC_API_URL", Value: "https://api.tickero.bg"}, // svc-level default
		{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},         // svc-level default
		{Name: "DATABASE_URL", Value: "subscribed"},                    // subscribed
	}
	overrides := []kube.KusoEnvVar{
		{Name: "NEXT_PUBLIC_API_URL", Value: "https://api-staging.tickero.bg"}, // per-env override
		{Name: "NEXT_PUBLIC_SITE_URL", Value: "https://staging.tickero.bg"},    // per-env-only
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
