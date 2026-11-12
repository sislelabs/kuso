package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func connRef(name, conn string) kube.KusoEnvVar {
	return kube.KusoEnvVar{Name: name, ValueFrom: map[string]any{
		"secretKeyRef": map[string]any{"name": conn, "key": "DATABASE_URL"},
	}}
}

func refConn(t *testing.T, vars []kube.KusoEnvVar, name string) string {
	t.Helper()
	for _, v := range vars {
		if v.Name == name {
			skr, _ := v.ValueFrom["secretKeyRef"].(map[string]any)
			n, _ := skr["name"].(string)
			return n
		}
	}
	t.Fatalf("%s not in %+v", name, vars)
	return ""
}

// The staging clone "acme-db-staging" was cloned from "acme-main" (the source
// was renamed/replaced since); "acme-db" is a newer production addon with no
// staging clone. Name derivation maps acme-db-conn onto main's clone and
// leaves acme-main-conn on PRODUCTION. The recorded source→clone map gets
// both right.
func TestRescopeAddonConnRefsByOrigin_RenamedSource(t *testing.T) {
	t.Parallel()
	in := []kube.KusoEnvVar{connRef("DATABASE_URL", "acme-main-conn"), connRef("OLD_URL", "acme-db-conn")}
	out := rescopeAddonConnRefsByOrigin(in, map[string]string{"acme-main-conn": "acme-db-staging-conn"})
	if got := refConn(t, out, "DATABASE_URL"); got != "acme-db-staging-conn" {
		t.Errorf("DATABASE_URL -> %q, want the recorded clone acme-db-staging-conn", got)
	}
	if got := refConn(t, out, "OLD_URL"); got != "acme-db-conn" {
		t.Errorf("OLD_URL -> %q, must stay: acme-db has no clone", got)
	}
	if got := refConn(t, in, "DATABASE_URL"); got != "acme-main-conn" {
		t.Errorf("input mutated: %q", got)
	}
}

// Same scenario through service→env propagation, which derives the map from
// the clone CRs' recorded provenance instead of their names.
func TestPropagate_RescopesAddonRefsByRecordedOrigin(t *testing.T) {
	t.Parallel()
	clone := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db-staging", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "acme-db-staging",
			Namespace:   "kuso",
			Labels:      map[string]string{labelProject: "acme", labelEnv: "staging"},
			Annotations: map[string]string{"kuso.sislelabs.com/env-group-source-addon": "acme-main"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres"},
	})
	vars := []kube.KusoEnvVar{connRef("DATABASE_URL", "acme-main-conn"), connRef("OLD_URL", "acme-db-conn")}
	s := fakeServiceWithSecrets(t, nil,
		seedProject("acme", kube.KusoProjectSpec{}),
		seedService("acme", "api", kube.KusoServiceSpec{Project: "acme", EnvVars: vars}),
		seedEnv("acme", "api", "staging", "main", "acme-api-staging"),
		seedAddon("acme", "main", "postgres"),
		seedAddon("acme", "db", "postgres"),
		clone,
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"acme-db-conn", "acme-main-conn"}, nil
	}
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	env.Spec.EnvFromSecrets = []string{"acme-db-staging-conn"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", env); err != nil {
		t.Fatal(err)
	}
	svc, err := s.Kube.GetKusoService(context.Background(), "kuso", "acme-api")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.propagateChangedToEnvs(context.Background(), "kuso", "acme", "api", svc, changedFields{EnvVars: true}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "acme-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	if got := refConn(t, after.Spec.EnvVars, "DATABASE_URL"); got != "acme-db-staging-conn" {
		t.Errorf("DATABASE_URL -> %q, want acme-db-staging-conn (clone of acme-main)", got)
	}
	// acme-db has no staging clone, so OLD_URL is neither mis-mapped onto
	// main's clone nor left on the production conn: it's withheld (ISO-2).
	for _, v := range after.Spec.EnvVars {
		if v.Name == "OLD_URL" {
			t.Errorf("OLD_URL kept as %+v, want it withheld from isolated staging", v.ValueFrom)
		}
	}
}

// ISO-1 (2026-11-12): a service-level env save must not re-stamp the
// production refs a preview had withheld: an external DB is dropped unless
// previewEnvVars opts in, a native bucket goes through its read-only user.
func TestPropagate_PreviewKeepsProductionRefsWithheld(t *testing.T) {
	t.Parallel()
	psdb := typedSeed(kube.GVRAddons, "KusoAddon", "acme-psdb", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "acme-psdb", Namespace: "kuso", Labels: map[string]string{labelProject: "acme"}},
		Spec:       kube.KusoAddonSpec{Project: "acme", Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "psdb-creds"}},
	})
	vars := []kube.KusoEnvVar{
		connRef("PSDB_URL", "acme-psdb-conn"),
		connRef("S3_SECRET", "acme-storage-conn"),
		connRef("OPTED_URL", "acme-psdb-conn"),
	}
	svcSpec := kube.KusoServiceSpec{Project: "acme", EnvVars: vars,
		Previews: &kube.KusoServicePreviews{PreviewEnvVars: []kube.KusoEnvVar{connRef("OPTED_URL", "acme-psdb-conn")}}}
	s := fakeServiceWithSecrets(t, nil,
		seedProject("acme", kube.KusoProjectSpec{}),
		seedService("acme", "api", svcSpec),
		seedEnv("acme", "api", "preview-pr-7", "feat", "acme-api-pr-7"),
		seedAddon("acme", "storage", "s3"),
		psdb,
	)
	ctx := context.Background()
	svc, err := s.Kube.GetKusoService(ctx, "kuso", "acme-api")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.propagateChangedToEnvs(ctx, "kuso", "acme", "api", svc, changedFields{EnvVars: true}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	pr, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-api-pr-7")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range pr.Spec.EnvVars {
		if v.Name == "PSDB_URL" {
			t.Errorf("PSDB_URL re-stamped onto the preview as %+v", v.ValueFrom)
		}
	}
	if got := refConn(t, pr.Spec.EnvVars, "S3_SECRET"); got != "acme-storage-readonly" {
		t.Errorf("S3_SECRET -> %q, want the read-only acme-storage-readonly", got)
	}
	if got := refConn(t, pr.Spec.EnvVars, "OPTED_URL"); got != "acme-psdb-conn" {
		t.Errorf("OPTED_URL -> %q, want the previewEnvVars opt-in kept", got)
	}
}

// ISO-2 (2026-11-12): a service-level env write must not point an isolated
// staging env at a production postgres it has no clone of, while a
// production ref the env already read stays.
func TestPropagate_WithholdsUnclonedProductionRefsFromStaging(t *testing.T) {
	t.Parallel()
	clone := typedSeed(kube.GVRAddons, "KusoAddon", "acme-db-staging", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "acme-db-staging",
			Namespace:   "kuso",
			Labels:      map[string]string{labelProject: "acme", labelEnv: "staging"},
			Annotations: map[string]string{"kuso.sislelabs.com/env-group-source-addon": "acme-db"},
		},
		Spec: kube.KusoAddonSpec{Project: "acme", Kind: "postgres"},
	})
	vars := []kube.KusoEnvVar{
		connRef("DATABASE_URL", "acme-db-conn"),
		connRef("ANALYTICS_URL", "acme-analytics-conn"),
		connRef("CACHE_URL", "acme-cache-conn"),
	}
	s := fakeServiceWithSecrets(t, nil,
		seedProject("acme", kube.KusoProjectSpec{}),
		seedService("acme", "api", kube.KusoServiceSpec{Project: "acme", EnvVars: vars}),
		seedEnv("acme", "api", "staging", "main", "acme-api-staging"),
		seedEnv("acme", "api", "production", "main", "acme-api-production"),
		seedAddon("acme", "db", "postgres"),
		seedAddon("acme", "analytics", "postgres"),
		seedAddon("acme", "cache", "redis"),
		clone,
	)
	ctx := context.Background()
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	// Staging was set up sharing the production cache on purpose.
	env.Spec.EnvFromSecrets = []string{"acme-db-staging-conn", "acme-cache-conn"}
	if _, err := s.Kube.UpdateKusoEnvironment(ctx, "kuso", env); err != nil {
		t.Fatal(err)
	}
	svc, err := s.Kube.GetKusoService(ctx, "kuso", "acme-api")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.propagateChangedToEnvs(ctx, "kuso", "acme", "api", svc, changedFields{EnvVars: true}); err != nil {
		t.Fatalf("propagate: %v", err)
	}
	staging, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	if got := refConn(t, staging.Spec.EnvVars, "DATABASE_URL"); got != "acme-db-staging-conn" {
		t.Errorf("DATABASE_URL -> %q, want the staging clone", got)
	}
	if got := refConn(t, staging.Spec.EnvVars, "CACHE_URL"); got != "acme-cache-conn" {
		t.Errorf("CACHE_URL -> %q, want the production cache staging already mounts", got)
	}
	for _, v := range staging.Spec.EnvVars {
		if v.Name == "ANALYTICS_URL" {
			t.Errorf("ANALYTICS_URL reached staging as %+v; staging has no clone of analytics", v.ValueFrom)
		}
	}
	prod, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "acme-api-production")
	if err != nil {
		t.Fatal(err)
	}
	if got := refConn(t, prod.Spec.EnvVars, "ANALYTICS_URL"); got != "acme-analytics-conn" {
		t.Errorf("production ANALYTICS_URL -> %q, want acme-analytics-conn", got)
	}
}
