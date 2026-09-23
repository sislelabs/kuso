package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// envFrom is last-source-wins and every postgres conn publishes the same
// keys. PreserveEnvFromOrder appends names new to the env, so adding a
// project addon (psdb) to a staging env that already mounts its own clone
// would put psdb after db-staging and hand staging production's
// DIRECT_URL. The refresh must move the env's clone back to the end.
func TestRefreshEnvSecrets_NewProjectAddonKeepsOwnCloneLast(t *testing.T) {
	t.Parallel()

	addon := func(name string, labels map[string]string) seed {
		l := map[string]string{"kuso.sislelabs.com/project": "tickero"}
		for k, v := range labels {
			l[k] = v
		}
		return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "tickero-" + name, Namespace: "kuso", Labels: l},
			Spec:       kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		})
	}
	s := fakeService(t,
		seedProj("tickero"),
		addon("db", nil),
		addon("db-staging", map[string]string{kube.LabelEnv: "staging"}),
		addon("psdb", nil), // the addon just added to the project
		seedEnvWithSecrets("tickero", "api", "staging", "tickero-api-staging",
			[]string{"db", "psdb"},
			[]string{"tickero-api-secrets", "tickero-api-staging-secrets", "tickero-db-staging-conn"}),
	)

	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-staging")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	got := after.Spec.EnvFromSecrets
	if !slices.Contains(got, "tickero-psdb-conn") {
		t.Fatalf("new subscribed addon not mounted: %v", got)
	}
	if got[len(got)-1] != "tickero-db-staging-conn" {
		t.Fatalf("env's own clone must be mounted last or a project addon overrides its DATABASE_URL/DIRECT_URL: %v", got)
	}
}
