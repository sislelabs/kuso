package builds

import (
	"testing"

	"kuso/server/internal/kube"
)

func TestAddonNamesFromEnvFromSecrets(t *testing.T) {
	env := &kube.KusoEnvironment{}
	env.Spec.EnvFromSecrets = []string{"acme-db-conn", "acme-cache-conn", "some-shared-secret"}
	got := addonNamesFromEnvFromSecrets(env)
	// only the "-conn" suffixed entries map to addon names
	want := map[string]bool{"acme-db": true, "acme-cache": true}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 addon names", got)
	}
	for _, n := range got {
		if !want[n] {
			t.Errorf("unexpected addon name %q", n)
		}
	}
}
