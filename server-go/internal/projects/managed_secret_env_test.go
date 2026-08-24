package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

// TestGetEnv_SurfacesManagedSecretKeys is the end-to-end read test for the
// endpoint the web editor + CLI env list consume: GetEnv must surface keys
// that live only in the kuso-managed <service>-secrets envFrom mount (e.g.
// WETRAVEL_API_KEY from the coolify import), tagged managed-secret.
func TestGetEnv_SurfacesManagedSecretKeys(t *testing.T) {
	t.Parallel()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ServiceSecretName("alpha", "web"), Namespace: "kuso"},
		Data: map[string][]byte{
			"WETRAVEL_API_KEY":    []byte("k"),
			"INTERNAL_JWT_SECRET": []byte("j"),
		},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{secret},
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080,
			EnvVars: []kube.KusoEnvVar{{Name: "NODE_ENV", Value: "production"}}}),
	)
	out, err := s.GetEnv(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetEnv: %v", err)
	}
	got := map[string]string{} // name -> source
	for _, e := range out {
		got[e.Name] = e.Source
	}
	if got["WETRAVEL_API_KEY"] != managedSecretSource {
		t.Errorf("WETRAVEL_API_KEY source = %q, want managed-secret (surfaced from <service>-secrets)", got["WETRAVEL_API_KEY"])
	}
	if _, ok := got["NODE_ENV"]; !ok {
		t.Errorf("existing literal NODE_ENV missing from GetEnv output")
	}
}

// TestMergeAddonSecretKeys_SurfacesAndOverrides pins the addon-var contract.
// Addon conn keys reach the pod via an envFrom mount with no spec.envVars
// entry, so they were invisible in the editor — users went looking for
// DATABASE_URL and found nothing. They must surface as addon-secret rows,
// AND stop surfacing once the user overrides one (an override is a normal
// spec.envVars entry of the same name, which k8s gives precedence over
// envFrom, so the addon Secret is never written).
func TestMergeAddonSecretKeys_SurfacesAndOverrides(t *testing.T) {
	keys := []string{"DATABASE_URL", "POSTGRES_PASSWORD", "POOLER_URL"}

	// Nothing set yet: every addon key surfaces, tagged with its addon.
	got := mergeAddonSecretKeys(nil, "proj-db-conn", "db", keys)
	if len(got) != 3 {
		t.Fatalf("want 3 surfaced addon keys, got %d (%v)", len(got), got)
	}
	for _, e := range got {
		if e.Source != addonSecretSource {
			t.Errorf("%s: Source = %q, want %q", e.Name, e.Source, addonSecretSource)
		}
		if e.Addon != "db" {
			t.Errorf("%s: Addon = %q, want \"db\"", e.Name, e.Addon)
		}
		if e.Value != "" {
			t.Errorf("%s: Value must stay blank (caller resolves/masks), got %q", e.Name, e.Value)
		}
	}

	// User overrode DATABASE_URL: a real spec.envVars literal exists, so the
	// addon key must NOT be re-surfaced — the row is now a normal var.
	existing := []kube.KusoEnvVar{{Name: "DATABASE_URL", Value: "postgres://override"}}
	got = mergeAddonSecretKeys(existing, "proj-db-conn", "db", keys)
	names := map[string]int{}
	for _, e := range got {
		names[e.Name]++
	}
	if names["DATABASE_URL"] != 1 {
		t.Errorf("overridden key must appear exactly once, got %d", names["DATABASE_URL"])
	}
	for _, e := range got {
		if e.Name == "DATABASE_URL" && e.Source == addonSecretSource {
			t.Error("overridden DATABASE_URL must render as a normal var, not addon-secret")
		}
	}
}

// TestAddonConnSecretNames maps <addon>-conn mounts to their short names and
// ignores the kuso-managed <svc>-secrets mounts (those are a different path).
func TestAddonConnSecretNames(t *testing.T) {
	got := addonConnSecretNames(
		[]string{"saiton-db-conn", "saiton-sites-conn", "saiton-saiton-secrets", "saiton-saiton-production-secrets"},
		"saiton",
	)
	if len(got) != 2 {
		t.Fatalf("want 2 addon conn mounts, got %d (%v)", len(got), got)
	}
	if got["saiton-db-conn"] != "db" {
		t.Errorf("saiton-db-conn -> %q, want \"db\"", got["saiton-db-conn"])
	}
	if got["saiton-sites-conn"] != "sites" {
		t.Errorf("saiton-sites-conn -> %q, want \"sites\"", got["saiton-sites-conn"])
	}
	if _, bad := got["saiton-saiton-secrets"]; bad {
		t.Error("managed service secret must not be treated as an addon conn mount")
	}
}
