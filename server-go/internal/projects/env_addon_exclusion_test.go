package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// `env add api staging --addons postgres` on a service subscribed to
// [db, redis] mounted only db-staging at create; the next env-var
// propagation re-added the PRODUCTION redis conn as a "missing"
// subscription.
func TestAddEnvironment_UnclonedAddonStaysUnmountedAfterPropagation(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Runtime: "dockerfile", Port: 3000,
			SubscribedAddons: []string{"db", "redis"}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "db", "postgres"),
		seedAddon("alpha", "redis", "redis"),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-redis-conn"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-db-staging-conn"}, &got)
	ctx := context.Background()

	env, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging", Addons: []string{"postgres"}})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	if slices.Contains(env.Spec.EnvFromSecrets, "alpha-redis-conn") {
		t.Fatalf("fixture: production redis mounted at create: %v", env.Spec.EnvFromSecrets)
	}

	if _, err := s.SetEnvVar(ctx, "alpha", "web", "FOO", SetEnvVarRequest{Value: "bar"}); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(ctx, "kuso", env.Name)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(after.Spec.EnvFromSecrets, "alpha-redis-conn") {
		t.Errorf("staging mounts PRODUCTION redis after propagation: %v", after.Spec.EnvFromSecrets)
	}
	if !slices.Contains(after.Spec.EnvFromSecrets, "alpha-db-staging-conn") {
		t.Errorf("staging lost its own db clone: %v", after.Spec.EnvFromSecrets)
	}
	// Production keeps both subscribed conns.
	prod, _ := s.Kube.GetKusoEnvironment(ctx, "kuso", "alpha-web-production")
	if !slices.Contains(prod.Spec.EnvFromSecrets, "alpha-redis-conn") {
		t.Errorf("production lost redis: %v", prod.Spec.EnvFromSecrets)
	}
}
