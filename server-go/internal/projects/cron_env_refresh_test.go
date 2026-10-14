package projects

import (
	"context"
	"slices"
	"testing"

	"kuso/server/internal/kube"
)

// A cron snapshots its env at create and was only refreshed when a build
// promoted a new image tag, so `kuso env set` / a new addon never reached it.
func TestEnvChange_RefreshesServiceCronEnv(t *testing.T) {
	t.Parallel()
	img := &kube.KusoImage{Repository: "r", Tag: "abc"}
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080, SharedEnvKeys: []string{}}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedCron("alpha-web-nightly", kube.KusoCronSpec{Project: "alpha", Service: "alpha-web", Schedule: "0 0 * * *", Image: img, PinImage: true}),
		seedCron("alpha-api-nightly", kube.KusoCronSpec{Project: "alpha", Service: "alpha-api", Schedule: "0 0 * * *"}),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) { return []string{"alpha-db-conn"}, nil }
	ctx := context.Background()

	if _, err := s.SetEnvVar(ctx, "alpha", "web", "DATABASE_URL", SetEnvVarRequest{Value: "${{ db.DATABASE_URL }}"}); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	c, err := s.Kube.GetKusoCron(ctx, "kuso", "alpha-web-nightly")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(c.Spec.Env, func(e kube.KusoRunEnv) bool { return e.Name == "DATABASE_URL" }) {
		t.Errorf("cron env not refreshed: %+v", c.Spec.Env)
	}
	if c.Spec.Image == nil || c.Spec.Image.Tag != "abc" {
		t.Errorf("env refresh must not touch the pinned image: %+v", c.Spec.Image)
	}
	other, _ := s.Kube.GetKusoCron(ctx, "kuso", "alpha-api-nightly")
	if len(other.Spec.Env) != 0 {
		t.Errorf("another service's cron was touched: %+v", other.Spec.Env)
	}

	// Literal values land in the managed secret; the cron must mount it.
	stripeVal := "sk_live"
	if _, err := s.SetEnvVar(ctx, "alpha", "web", "STRIPE_KEY", SetEnvVarRequest{SecretValue: &stripeVal}); err != nil {
		t.Fatalf("SetEnvVar secret: %v", err)
	}
	c, _ = s.Kube.GetKusoCron(ctx, "kuso", "alpha-web-nightly")
	if !slices.Contains(c.Spec.EnvFromSecrets, "alpha-web-secrets") {
		t.Errorf("cron does not mount the managed secret: %v", c.Spec.EnvFromSecrets)
	}
}
