package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedAddon seeds a project addon CR (so statefulAddonKinds / postgresConnForEnv
// can see it). Mirrors the addons-package helper but in this package's harness.
func seedAddon(project, short, kind string) seed {
	return typedSeed(kube.GVRAddons, "KusoAddon", project+"-"+short, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + short,
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: project},
		},
		Spec: kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// fakeEnvAddons returns an EnvAddons func that records its call and returns the
// given clone conn-secrets, so AddEnvironment's swap logic can be asserted.
func fakeEnvAddons(clones []string, got *struct {
	scope   string
	kinds   []string
	seedAll bool
	called  bool
}) func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, error) {
	return func(ctx context.Context, project, envScope string, kinds []string, seedAll bool) ([]string, error) {
		got.scope = envScope
		got.kinds = kinds
		got.seedAll = seedAll
		got.called = true
		return clones, nil
	}
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestAddEnvironment_SwapsToPerEnvAddons: a new named env drops the project addon
// conn-secrets from EnvFromSecrets and appends the per-env clone conn-secrets.
func TestAddEnvironment_SwapsToPerEnvAddons(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	// The project's shared addon conn-secrets (what a shared env would mount).
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn", "alpha-shared"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-pg-staging-conn"}, &got)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}

	if !got.called {
		t.Fatalf("EnvAddons was not called for a default (non-share-addons) env")
	}
	if got.scope != "staging" {
		t.Fatalf("EnvAddons scope = %q, want staging", got.scope)
	}
	// Default kinds = the project's stateful kinds (postgres here).
	if !contains(got.kinds, "postgres") {
		t.Fatalf("EnvAddons kinds = %v, want to include postgres", got.kinds)
	}
	if got.seedAll {
		t.Fatalf("seedAll should be false without --seed-from")
	}

	efs := env.Spec.EnvFromSecrets
	if contains(efs, "alpha-pg-conn") {
		t.Fatalf("project addon conn alpha-pg-conn should have been dropped: %v", efs)
	}
	if !contains(efs, "alpha-pg-staging-conn") {
		t.Fatalf("per-env clone conn alpha-pg-staging-conn missing: %v", efs)
	}
}

// TestAddEnvironment_ShareAddonsKeepsProjectConns: --share-addons keeps the
// shared project addon conn-secrets and does NOT provision per-env addons.
func TestAddEnvironment_ShareAddonsKeepsProjectConns(t *testing.T) {
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	var got struct {
		scope   string
		kinds   []string
		seedAll bool
		called  bool
	}
	s.EnvAddons = fakeEnvAddons([]string{"alpha-pg-staging-conn"}, &got)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "staging", ShareAddons: true})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	if got.called {
		t.Fatalf("EnvAddons should NOT be called with --share-addons")
	}
	if !contains(env.Spec.EnvFromSecrets, "alpha-pg-conn") {
		t.Fatalf("shared project addon conn should be kept: %v", env.Spec.EnvFromSecrets)
	}
	if contains(env.Spec.EnvFromSecrets, "alpha-pg-staging-conn") {
		t.Fatalf("no per-env clone should be mounted with --share-addons: %v", env.Spec.EnvFromSecrets)
	}
}
