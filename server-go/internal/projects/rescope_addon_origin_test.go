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
	if got := refConn(t, after.Spec.EnvVars, "OLD_URL"); got != "acme-db-conn" {
		t.Errorf("OLD_URL -> %q, must not be mis-mapped onto main's clone", got)
	}
}
