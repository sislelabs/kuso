package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// The pod template hashes envFrom in list order, so reordering the same set of
// secrets is a rollout. Two writers disagreed on the order: subscribing an
// addon appended its conn to the env's existing list, while the addon refresh
// (run on every addon event) rebuilt the list alphabetically. The first addon
// event after a subscribe therefore rolled every pod in the project for no
// change in content — on tickero it re-created the api and worker pods while
// the pooler was broken and turned a degraded pooler into a crashloop.
func TestRefreshEnvSecrets_KeepsExistingOrderWhenSetUnchanged(t *testing.T) {
	t.Parallel()

	addon := func(name string) seed {
		return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{
				Name: "tickero-" + name, Namespace: "kuso",
				Labels: map[string]string{"kuso.sislelabs.com/project": "tickero"},
			},
			Spec: kube.KusoAddonSpec{Project: "tickero", Kind: "postgres"},
		})
	}
	// tickero-api-production as it stood before the incident: psdb was
	// subscribed later and appended after the service secrets.
	before := []string{
		"tickero-cache-conn", "tickero-queue-conn", "tickero-storage-conn",
		"tickero-api-secrets", "tickero-api-production-secrets", "tickero-psdb-conn",
	}
	s := fakeService(t,
		seedProj("tickero"),
		addon("cache"), addon("psdb"), addon("queue"), addon("storage"),
		seedEnvWithSecrets("tickero", "api", "production", "tickero-api-production",
			[]string{"cache", "psdb", "queue", "storage"}, slices.Clone(before)),
	)

	// Production subscribes to shared keys individually, so the refresh must
	// not blanket-mount the shared secrets — keep the set identical.
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	envCR.Spec.SharedEnvKeys = []string{}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	if err := s.RefreshEnvSecrets(context.Background(), "tickero"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !slices.Equal(after.Spec.EnvFromSecrets, before) {
		t.Fatalf("refresh reordered an unchanged set (this rolls every pod):\n before %v\n after  %v", before, after.Spec.EnvFromSecrets)
	}
}
