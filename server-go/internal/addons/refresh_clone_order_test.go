package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// tickero-api-staging mounted its own clone (tickero-db-staging-conn) AND the
// project-level external addon (tickero-psdb-conn). Both publish DIRECT_URL;
// envFrom is last-source-wins; the list was alphabetical; "psdb" came last.
// The staging migration release hook ran against production's admin DSN.
//
// After a refresh the env's own clone must be ordered after every
// project-level conn so it wins.
func TestRefreshEnvSecrets_OwnCloneIsOrderedLast(t *testing.T) {
	t.Parallel()

	s := fakeService(t,
		seedProj("tickero"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "tickero-db", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"}},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "tickero-psdb", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"}},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres",
				External: &kube.KusoAddonExternal{SecretName: "tickero-psdb-external"}},
		}),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "tickero-db-staging", Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero", kube.LabelEnv: "staging"}},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		}),
		seedEnv("tickero", "api", "staging", "tickero-api-staging"),
	)
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	envCR.Spec.SubscribedAddons = []string{"db", "psdb"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatal(err)
	}

	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	got := after.Spec.EnvFromSecrets
	ci := slices.Index(got, "tickero-db-staging-conn")
	pi := slices.Index(got, "tickero-psdb-conn")
	if ci < 0 || pi < 0 {
		t.Fatalf("expected both the clone and the external conn to be mounted, got %v", got)
	}
	if ci < pi {
		t.Errorf("the env's own clone is listed BEFORE the project-level addon; last-source-wins hands "+
			"DIRECT_URL/POSTGRES_* to psdb, i.e. production.\nenvFromSecrets = %v", got)
	}
	if slices.Contains(got, "tickero-db-conn") {
		t.Errorf("staging mounted the production source of its clone: %v", got)
	}
}
