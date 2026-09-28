package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// The clone re-assert must honour the env's subscription: an env gets back
// only the clones of addons its service subscribes to. Before this, every
// addon add/delete handed a public frontend (subscribedAddons=[]) its env's
// DATABASE_URL and REDIS_URL again, undoing the env-create filter.
func TestRefreshEnvSecrets_CloneReassertHonoursSubscription(t *testing.T) {
	t.Parallel()

	addon := func(name, env string, annotations map[string]string) seed {
		labels := map[string]string{"kuso.sislelabs.com/project": "e2e"}
		if env != "" {
			labels[kube.LabelEnv] = env
		}
		return typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels, Annotations: annotations},
			Spec:       kube.KusoAddonSpec{Project: "e2e", Kind: "postgres"},
		})
	}
	s := fakeService(t,
		seedProj("e2e"),
		addon("e2e-db", "", nil),
		addon("e2e-cache", "", nil),
		addon("e2e-db-staging", "staging", nil),
		addon("e2e-cache-staging", "staging", nil),
		// env-group clone: source recorded on the CR, not derivable by name.
		addon("e2e-data-qa", "qa", map[string]string{"kuso.sislelabs.com/env-group-source-addon": "e2e-db"}),
		seedEnv("e2e", "web", "staging", "e2e-web-staging"),
		seedEnv("e2e", "api", "staging", "e2e-api-staging"),
		seedEnv("e2e", "legacy", "staging", "e2e-legacy-staging"),
		seedEnv("e2e", "api-qa", "qa", "e2e-api-qa-production"),
		seedEnv("e2e", "web-qa", "qa", "e2e-web-qa-production"),
	)
	subscribe := func(name string, subs []string) {
		env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		env.Spec.SubscribedAddons = subs
		if _, err := s.Kube.UpdateKusoEnvironment(context.Background(), "kuso", env); err != nil {
			t.Fatalf("update %s: %v", name, err)
		}
	}
	subscribe("e2e-web-staging", []string{})
	subscribe("e2e-api-staging", []string{"db"})
	subscribe("e2e-api-qa-production", []string{"db"})
	subscribe("e2e-web-qa-production", []string{"cache"})

	if err := s.RefreshEnvSecrets(context.Background(), "e2e"); err != nil {
		t.Fatalf("RefreshEnvSecrets: %v", err)
	}

	cases := []struct {
		env           string
		want, notWant []string
	}{
		{"e2e-web-staging", nil, []string{"e2e-db-staging-conn", "e2e-cache-staging-conn"}},
		{"e2e-api-staging", []string{"e2e-db-staging-conn"}, []string{"e2e-cache-staging-conn"}},
		{"e2e-legacy-staging", []string{"e2e-db-staging-conn", "e2e-cache-staging-conn"}, nil},
		{"e2e-api-qa-production", []string{"e2e-data-qa-conn"}, nil},
		{"e2e-web-qa-production", nil, []string{"e2e-data-qa-conn"}},
	}
	for _, tc := range cases {
		env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", tc.env)
		if err != nil {
			t.Fatalf("get %s: %v", tc.env, err)
		}
		efs := env.Spec.EnvFromSecrets
		for _, w := range tc.want {
			if !slices.Contains(efs, w) {
				t.Errorf("%s: want %s mounted: %v", tc.env, w, efs)
			}
		}
		for _, nw := range tc.notWant {
			if slices.Contains(efs, nw) {
				t.Errorf("%s: %s must not be mounted: %v", tc.env, nw, efs)
			}
		}
	}
}
