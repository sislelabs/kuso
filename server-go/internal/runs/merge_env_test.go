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

func TestMergeRunEnv_CarriesValueFromVars(t *testing.T) {
	// A ${{ }} alias (secretKeyRef) must be CARRIED into the run so the run sees
	// the service's aliased env (e.g. DATABASE_URI) — it resolves against the
	// same Secrets the run mounts via envFromSecrets. Previously dropped, which
	// silently broke seeds/migrations reading the aliased name.
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "db-conn", "key": "DATABASE_URL"}}
	svc := []kube.KusoEnvVar{
		{Name: "DATABASE_URI", ValueFrom: ref},
		{Name: "PLAIN", Value: "ok"},
	}
	got := mergeRunEnv(svc, nil)

	var alias *kube.KusoRunEnv
	for i := range got {
		if got[i].Name == "DATABASE_URI" {
			alias = &got[i]
		}
	}
	if alias == nil {
		t.Fatalf("valueFrom alias DATABASE_URI must be carried: %#v", got)
	}
	if alias.ValueFrom == nil {
		t.Fatalf("DATABASE_URI must keep its ValueFrom ref: %#v", *alias)
	}
	if alias.Value != "" {
		t.Fatalf("a valueFrom var must not also have a Value: %#v", *alias)
	}
	if names(got)["PLAIN"] != "ok" {
		t.Fatalf("plain var should remain: %#v", got)
	}
}

func TestMergeRunEnv_OverlayClearsValueFrom(t *testing.T) {
	// A plain --env overlay of the same name replaces a service valueFrom var
	// entirely (value + valueFrom can't coexist on one kube env var).
	ref := map[string]any{"secretKeyRef": map[string]any{"name": "db-conn", "key": "DATABASE_URL"}}
	svc := []kube.KusoEnvVar{{Name: "DATABASE_URI", ValueFrom: ref}}
	overlay := []EnvVar{{Name: "DATABASE_URI", Value: "postgres://override"}}
	got := mergeRunEnv(svc, overlay)
	if len(got) != 1 {
		t.Fatalf("expected 1 var, got %#v", got)
	}
	if got[0].Value != "postgres://override" {
		t.Fatalf("overlay value should win: %#v", got[0])
	}
	if got[0].ValueFrom != nil {
		t.Fatalf("overlay must clear ValueFrom (value+valueFrom can't coexist): %#v", got[0])
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

func TestMergeRunEnv_SkipsManagedSecretPlaceholders(t *testing.T) {
	// A managed-secret entry is a server-set DISPLAY hint enumerated from the
	// <service>-secrets envFrom mount: Source="managed-secret", empty Value,
	// no ValueFrom. Carrying it into the run's explicit env emits
	// `{name: K, value: ""}`, and an explicit env entry BEATS envFrom in kube —
	// so the run sees an empty string instead of the real secret. That made
	// `kuso run … -- sh -c 'echo $STRIPE_WEBHOOK_SECRET'` print the stale/empty
	// value even after `kuso secret set`, which reads as "the secret didn't
	// save" when in fact only the run's view was wrong.
	svc := []kube.KusoEnvVar{
		{Name: "STRIPE_WEBHOOK_SECRET", Source: "managed-secret"},
		{Name: "NODE_ENV", Value: "production"},
	}
	got := mergeRunEnv(svc, nil)
	for _, v := range got {
		if v.Name == "STRIPE_WEBHOOK_SECRET" {
			t.Fatalf("managed-secret placeholder must NOT be carried into run env "+
				"(it would shadow the envFrom mount with an empty value): %#v", v)
		}
	}
	if names(got)["NODE_ENV"] != "production" {
		t.Fatalf("normal vars must still be carried: %#v", got)
	}
}

func TestMergeRunEnv_OverlayStillWinsOverManagedSecret(t *testing.T) {
	// Dropping the placeholder must not break an explicit --env override of the
	// same name: the caller asked for that literal value, so it still applies.
	svc := []kube.KusoEnvVar{{Name: "STRIPE_WEBHOOK_SECRET", Source: "managed-secret"}}
	overlay := []EnvVar{{Name: "STRIPE_WEBHOOK_SECRET", Value: "whsec_override"}}
	got := mergeRunEnv(svc, overlay)
	if len(got) != 1 {
		t.Fatalf("expected exactly one entry, got %#v", got)
	}
	if got[0].Value != "whsec_override" {
		t.Fatalf("explicit overlay must still win: %#v", got[0])
	}
}

func TestMergeRunEnv_SkipsEmptyNames(t *testing.T) {
	got := mergeRunEnv([]kube.KusoEnvVar{{Name: "", Value: "nope"}}, []EnvVar{{Name: "", Value: "nope2"}})
	if len(got) != 0 {
		t.Fatalf("empty-named vars must be skipped, got %#v", got)
	}
}
