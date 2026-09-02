package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// Re-propagating an unchanged addon subscription must not reorder the env's
// envFromSecrets: the pod template hashes the list in order, so a reorder is a
// rollout with no change in content. Companion to the addon-refresh test in
// the addons package — both writers must agree on "same set, same order".
func TestPropagate_UnchangedSubscriptionKeepsEnvFromOrder(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("tickero", kube.KusoProjectSpec{}),
		seedService("tickero", "api", kube.KusoServiceSpec{SubscribedAddons: []string{"cache", "psdb", "queue", "storage"}}),
		seedEnv("tickero", "api", "production", "production", "tickero-api-production"),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-cache-conn", "tickero-psdb-conn", "tickero-queue-conn", "tickero-storage-conn"}, nil
	}
	before := []string{
		"tickero-cache-conn", "tickero-queue-conn", "tickero-storage-conn",
		"tickero-api-secrets", "tickero-api-production-secrets", "tickero-psdb-conn",
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatal(err)
	}
	envCR.Spec.EnvFromSecrets = slices.Clone(before)
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetSubscribedAddons(context.Background(), "tickero", "api", []string{"cache", "psdb", "queue", "storage"}); err != nil {
		t.Fatalf("SetSubscribedAddons: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-production")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(after.Spec.EnvFromSecrets, before) {
		t.Fatalf("propagation reordered an unchanged set (this rolls every pod):\n before %v\n after  %v", before, after.Spec.EnvFromSecrets)
	}
}
