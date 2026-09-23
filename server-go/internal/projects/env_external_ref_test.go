package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// An explicit DATABASE_URL ref to an external addon can't be rescoped (external
// addons are never cloned), and an explicit env entry wins over envFrom. A new
// isolated staging env would therefore run, and migrate, against the
// production database. AddEnvironment must refuse before provisioning anything.
func TestAddEnvironment_RefusesExplicitRefToUnclonedAddon(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "api", kube.KusoServiceSpec{
			Runtime: "dockerfile",
			Port:    3000,
			EnvVars: []kube.KusoEnvVar{{Name: "DATABASE_URL", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-psdb-conn", "key": "DATABASE_URL"},
			}}},
		}),
		seedEnv("alpha", "api", "production", "main", "alpha-api-production"),
		typedSeed(kube.GVRAddons, "KusoAddon", "alpha-psdb", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-psdb", Namespace: "kuso", Labels: map[string]string{labelProject: "alpha"}},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "postgres",
				External: &kube.KusoAddonExternal{SecretName: "alpha-psdb-external"}},
		}),
		seedAddon("alpha", "cache", "redis"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-cache-conn", "alpha-psdb-conn"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-cache-staging-conn"}, &got)

	_, err := s.AddEnvironment(context.Background(), "alpha", "api", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("AddEnvironment err = %v, want ErrInvalid for DATABASE_URL -> external alpha-psdb-conn", err)
	}
	if got.called {
		t.Errorf("env addons were provisioned before the refusal")
	}
	if _, gerr := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-staging"); gerr == nil {
		t.Errorf("staging env was created despite the refusal")
	}

	// --share-addons is the explicit opt-in to production addons.
	if _, err := s.AddEnvironment(context.Background(), "alpha", "api", CreateEnvRequest{Name: "qa", Branch: "qa", ShareAddons: true}); err != nil {
		t.Fatalf("AddEnvironment with ShareAddons: %v", err)
	}
}
