package runs

import (
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

// TestMergeRunEnv asserts the exact merged env (order included) for each
// service-vars + --env overlay combination.
func TestMergeRunEnv(t *testing.T) {
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "db-conn", "key": "DATABASE_URL"}}
	cases := []struct {
		name    string
		svc     []kube.KusoEnvVar
		overlay []EnvVar
		want    []kube.KusoRunEnv
	}{
		{
			name: "IncludesServicePlainVars",
			svc: []kube.KusoEnvVar{
				{Name: "INTERNAL_SYSTEM_URL", Value: "https://internal.example.com"},
				{Name: "NODE_ENV", Value: "production"},
			},
			want: []kube.KusoRunEnv{
				{Name: "INTERNAL_SYSTEM_URL", Value: "https://internal.example.com"},
				{Name: "NODE_ENV", Value: "production"},
			},
		},
		{
			// A ${{ }} alias (secretKeyRef) must be CARRIED into the run so the run sees
			// the service's aliased env (e.g. DATABASE_URI) — it resolves against the
			// same Secrets the run mounts via envFromSecrets. Previously dropped, which
			// silently broke seeds/migrations reading the aliased name.
			name: "CarriesValueFromVars",
			svc: []kube.KusoEnvVar{
				{Name: "DATABASE_URI", ValueFrom: ref},
				{Name: "PLAIN", Value: "ok"},
			},
			want: []kube.KusoRunEnv{
				{Name: "DATABASE_URI", ValueFrom: ref},
				{Name: "PLAIN", Value: "ok"},
			},
		},
		{
			// A plain --env overlay of the same name replaces a service valueFrom var
			// entirely (value + valueFrom can't coexist on one kube env var).
			name:    "OverlayClearsValueFrom",
			svc:     []kube.KusoEnvVar{{Name: "DATABASE_URI", ValueFrom: ref}},
			overlay: []EnvVar{{Name: "DATABASE_URI", Value: "postgres://override"}},
			want:    []kube.KusoRunEnv{{Name: "DATABASE_URI", Value: "postgres://override"}},
		},
		{
			name:    "OverlayWinsAndAdds",
			svc:     []kube.KusoEnvVar{{Name: "MIGRATE", Value: "false"}, {Name: "KEEP", Value: "1"}},
			overlay: []EnvVar{{Name: "MIGRATE", Value: "true"}, {Name: "EXTRA", Value: "x"}},
			want: []kube.KusoRunEnv{
				{Name: "MIGRATE", Value: "true"},
				{Name: "KEEP", Value: "1"},
				{Name: "EXTRA", Value: "x"},
			},
		},
		{
			// A managed-secret entry is a server-set DISPLAY hint enumerated from the
			// <service>-secrets envFrom mount: Source="managed-secret", empty Value,
			// no ValueFrom. Carrying it into the run's explicit env emits
			// `{name: K, value: ""}`, and an explicit env entry BEATS envFrom in kube —
			// so the run sees an empty string instead of the real secret. That made
			// `kuso run … -- sh -c 'echo $STRIPE_WEBHOOK_SECRET'` print the stale/empty
			// value even after `kuso secret set`, which reads as "the secret didn't
			// save" when in fact only the run's view was wrong.
			name: "SkipsManagedSecretPlaceholders",
			svc: []kube.KusoEnvVar{
				{Name: "STRIPE_WEBHOOK_SECRET", Source: "managed-secret"},
				{Name: "NODE_ENV", Value: "production"},
			},
			want: []kube.KusoRunEnv{{Name: "NODE_ENV", Value: "production"}},
		},
		{
			// Dropping the placeholder must not break an explicit --env override of the
			// same name: the caller asked for that literal value, so it still applies.
			name:    "OverlayStillWinsOverManagedSecret",
			svc:     []kube.KusoEnvVar{{Name: "STRIPE_WEBHOOK_SECRET", Source: "managed-secret"}},
			overlay: []EnvVar{{Name: "STRIPE_WEBHOOK_SECRET", Value: "whsec_override"}},
			want:    []kube.KusoRunEnv{{Name: "STRIPE_WEBHOOK_SECRET", Value: "whsec_override"}},
		},
		{
			name:    "SkipsEmptyNames",
			svc:     []kube.KusoEnvVar{{Name: "", Value: "nope"}},
			overlay: []EnvVar{{Name: "", Value: "nope2"}},
			want:    []kube.KusoRunEnv{},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeRunEnv(tc.svc, tc.overlay); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("mergeRunEnv =\n%#v\nwant\n%#v", got, tc.want)
			}
		})
	}
}
