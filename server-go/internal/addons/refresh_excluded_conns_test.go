package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// An env created with `--addons postgres` records the project conns it
// deliberately doesn't mount; an addon add/delete refresh must not hand it
// the PRODUCTION redis conn anyway.
func TestRefreshEnvSecrets_HonoursExcludedAddonConns(t *testing.T) {
	t.Parallel()
	addon := func(name, env, kind string) seed {
		labels := map[string]string{"kuso.sislelabs.com/project": "e2e"}
		if env != "" {
			labels[kube.LabelEnv] = env
		}
		return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels},
			Spec:       kube.KusoAddonSpec{Project: "e2e", Kind: kind},
		})
	}
	s := fakeService(t,
		seedProj("e2e"),
		addon("e2e-db", "", "postgres"),
		addon("e2e-cache", "", "redis"),
		addon("e2e-db-staging", "staging", "postgres"),
		seedEnv("e2e", "api", "staging", "e2e-api-staging"),
	)
	ctx := context.Background()
	env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "e2e-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	env.Annotations = map[string]string{AnnotationExcludedAddonConns: "e2e-cache-conn"}
	env.Spec.SubscribedAddons = []string{"db", "cache"}
	if _, err := s.Kube.UpdateKusoEnvironment(ctx, "kuso", env); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshEnvSecrets(ctx, "e2e"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}
	got, err := s.Kube.GetKusoEnvironment(ctx, "kuso", "e2e-api-staging")
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got.Spec.EnvFromSecrets, "e2e-cache-conn") {
		t.Errorf("excluded production conn re-mounted: %v", got.Spec.EnvFromSecrets)
	}
	if !slices.Contains(got.Spec.EnvFromSecrets, "e2e-db-staging-conn") {
		t.Errorf("own clone missing: %v", got.Spec.EnvFromSecrets)
	}
}
