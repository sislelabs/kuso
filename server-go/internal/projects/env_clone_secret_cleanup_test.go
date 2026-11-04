package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// LIFE-10: an env-group clone CR is <p>-<svc>-<group>-production but
// mounts EnvSecretName(p, "<svc>-<group>", <group>). Deleting it cleaned
// up the name-derived "production" scope and orphaned the real Secret, so
// re-creating the group inherited the old per-env values.
func TestDeleteEnvironment_CloneCleansItsEnvScopedSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api-staging", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api-staging", "staging", "dev", "alpha-api-staging-production"),
	)
	var cleaned []string
	s.SecretsCleanupForEnv = func(_ context.Context, p, svc, e string) error {
		cleaned = append(cleaned, kube.EnvSecretName(p, svc, e))
		return nil
	}
	if err := s.deleteEnvironment(context.Background(), "alpha", "alpha-api-staging-production", false); err != nil {
		t.Fatalf("deleteEnvironment: %v", err)
	}
	want := kube.EnvSecretName("alpha", "api-staging", "staging")
	if len(cleaned) != 1 || cleaned[0] != want {
		t.Fatalf("cleaned env secrets = %v, want [%s]", cleaned, want)
	}
}
