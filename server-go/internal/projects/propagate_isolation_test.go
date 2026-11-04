package projects

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedIsolationAddon(short, kind, envScope string, external bool) seed {
	labels := map[string]string{labelProject: "tickero"}
	if envScope != "" {
		labels[labelEnv] = envScope
	}
	spec := kube.KusoAddonSpec{Project: "tickero", Kind: kind}
	if external {
		spec.External = &kube.KusoAddonExternal{SecretName: short + "-src"}
	}
	return typedSeed(kube.GVRAddons, "KusoAddon", "tickero-"+short, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "tickero-" + short, Namespace: "kuso", Labels: labels},
		Spec:       spec,
	})
}

// DATA-03/DATA-04: subscribing a service to an addon must not mount the
// PRODUCTION conn on its preview (no clone) or add a new postgres conn to an
// isolated staging env.
func TestSetSubscribedAddons_IsolatedEnvsDoNotGainProductionConns(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("tickero", kube.KusoProjectSpec{}),
		seedService("tickero", "api", kube.KusoServiceSpec{SubscribedAddons: []string{"db"}}),
		seedEnv("tickero", "api", "preview-pr-52", "flow/x", "tickero-api-pr-52"),
		seedEnv("tickero", "api", "staging", "dev", "tickero-api-staging"),
		seedIsolationAddon("db", "postgres", "", false),
		seedIsolationAddon("db-pr-52", "postgres", "preview-pr-52", false),
		seedIsolationAddon("db-staging", "postgres", "staging", false),
		seedIsolationAddon("psdb", "postgres", "", true),
		seedIsolationAddon("analytics", "postgres", "", false),
		seedIsolationAddon("storage", "s3", "", false),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-analytics-conn", "tickero-db-conn", "tickero-psdb-conn", "tickero-storage-conn"}, nil
	}
	s.ReferenceableConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-analytics-conn", "tickero-db-conn", "tickero-psdb-conn", "tickero-storage-conn",
			"tickero-db-pr-52-conn", "tickero-db-staging-conn"}, nil
	}
	if _, err := s.SetSubscribedAddons(context.Background(), "tickero", "api", []string{"db", "psdb", "analytics", "storage"}); err != nil {
		t.Fatalf("SetSubscribedAddons: %v", err)
	}
	get := func(name string) []string {
		env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
		if err != nil {
			t.Fatal(err)
		}
		return env.Spec.EnvFromSecrets
	}
	preview := get("tickero-api-pr-52")
	for _, leak := range []string{"tickero-db-conn", "tickero-psdb-conn", "tickero-analytics-conn"} {
		if slices.Contains(preview, leak) {
			t.Errorf("preview mounts production %s: %v", leak, preview)
		}
	}
	if !slices.Contains(preview, "tickero-db-pr-52-conn") || !slices.Contains(preview, "tickero-storage-conn") {
		t.Errorf("preview lost its clone or shared s3: %v", preview)
	}
	staging := get("tickero-api-staging")
	if slices.Contains(staging, "tickero-analytics-conn") || slices.Contains(staging, "tickero-db-conn") {
		t.Errorf("isolated staging gained a production postgres conn: %v", staging)
	}
	if !slices.Contains(staging, "tickero-psdb-conn") {
		t.Errorf("external addons stay shared with named envs: %v", staging)
	}
}
