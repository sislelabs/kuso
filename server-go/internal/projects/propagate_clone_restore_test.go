package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// Service-driven propagation rebuilt an env's envFromSecrets from the env's
// CURRENT list, so it could preserve a clone but never restore one. The
// addon-driven refresh got a re-assert-from-labels fix (v0.25.9); this path
// didn't, and preview envs kept losing their clone on every `env set` /
// subscribe until an explicit ${{ db-pr-N.* }} ref masked it.
func TestPropagate_RestoresEnvCloneConn(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("tickero", kube.KusoProjectSpec{}),
		seedService("tickero", "api", kube.KusoServiceSpec{SubscribedAddons: []string{"db"}}),
		seedEnv("tickero", "api", "preview-pr-52", "flow/x", "tickero-api-pr-52"),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-cache-conn", "tickero-db-conn", "tickero-psdb-conn"}, nil
	}
	s.ReferenceableConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"tickero-cache-conn", "tickero-db-conn", "tickero-psdb-conn", "tickero-db-pr-52-conn"}, nil
	}
	envCR, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-pr-52")
	if err != nil {
		t.Fatal(err)
	}
	// The damaged state: the clone is gone, the production source has crept in.
	envCR.Spec.EnvFromSecrets = []string{"tickero-cache-conn", "tickero-db-conn", "tickero-api-secrets"}
	if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", envCR); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetSubscribedAddons(context.Background(), "tickero", "api", []string{"db", "psdb", "cache"}); err != nil {
		t.Fatalf("SetSubscribedAddons: %v", err)
	}

	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "tickero-api-pr-52")
	if err != nil {
		t.Fatal(err)
	}
	got := after.Spec.EnvFromSecrets
	if !slices.Contains(got, "tickero-db-pr-52-conn") {
		t.Errorf("the preview's own clone was not restored: %v", got)
	}
	if slices.Contains(got, "tickero-db-conn") {
		t.Errorf("the preview still mounts the PRODUCTION source its clone replaces: %v", got)
	}
	if !slices.Contains(got, "tickero-psdb-conn") || !slices.Contains(got, "tickero-cache-conn") {
		t.Errorf("subscribed project-level addons must still mount: %v", got)
	}
	if ci, pi := slices.Index(got, "tickero-db-pr-52-conn"), slices.Index(got, "tickero-psdb-conn"); ci >= 0 && pi >= 0 && ci < pi {
		t.Errorf("clone must be ordered after project-level conns: %v", got)
	}
}
