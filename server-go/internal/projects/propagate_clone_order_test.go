package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// The exact incident: tickero/api was subscribed to the external `psdb` addon
// at SERVICE level. Propagation appended tickero-psdb-conn to every env's
// envFromSecrets — after staging's own tickero-db-staging-conn. Both publish
// DIRECT_URL; envFrom is last-source-wins; staging's migration release hook
// ran against production's admin DSN.
//
// Subscribing at service level is legitimate (production needs psdb). The env's
// own clone simply has to come last so it wins the key collision.
func TestPropagate_SubscribingProjectAddonKeepsEnvCloneLast(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("tickero", kube.KusoProjectSpec{}),
		seedService("tickero", "api", kube.KusoServiceSpec{SubscribedAddons: []string{"db"}}),
		seedEnv("tickero", "api", "staging", "stage", "tickero-api-staging"),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-db-conn", "tickero-psdb-conn"}, nil
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	envCR.Spec.EnvFromSecrets = []string{"tickero-cache-staging-conn", "tickero-db-staging-conn", "tickero-api-secrets"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetSubscribedAddons(context.Background(), "tickero", "api", []string{"db", "psdb"}); err != nil {
		t.Fatalf("SetSubscribedAddons: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	got := after.Spec.EnvFromSecrets
	ci, pi := slices.Index(got, "tickero-db-staging-conn"), slices.Index(got, "tickero-psdb-conn")
	if ci < 0 || pi < 0 {
		t.Fatalf("expected both the clone and psdb mounted on staging, got %v", got)
	}
	if ci < pi {
		t.Errorf("staging's own clone is listed before tickero-psdb-conn — last-source-wins hands "+
			"DIRECT_URL to psdb, i.e. production's admin DSN.\nenvFromSecrets = %v", got)
	}
}
