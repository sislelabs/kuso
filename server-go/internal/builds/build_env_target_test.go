package builds

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// seedStagingEnv seeds a custom env on branch develop whose DATABASE_URL
// was re-scoped onto the staging clone (what AddEnvironment does).
func seedStagingEnv(project, service string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-staging",
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelService: service,
				kube.LabelEnv:     "staging",
			},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project: project, Service: project + "-" + service, Kind: "custom", Branch: "develop",
			EnvVars: []kube.KusoEnvVar{
				{Name: "DATABASE_URL", ValueFrom: map[string]any{
					"secretKeyRef": map[string]any{"name": "alpha-db-staging-conn", "key": "url"},
				}},
			},
		},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

func TestCreate_StagingBranchBakesStagingEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedServiceWithSecretEnv("alpha", "web", "alpha-db-conn", "url"),
		seedProductionEnv("alpha", "web"),
		seedStagingEnv("alpha", "web"),
	)
	ctx := context.Background()
	mkSecret(t, s, "alpha-db-conn", "url", "postgres://PROD/db")
	mkSecret(t, s, "alpha-db-staging-conn", "url", "postgres://STAGING/db")
	got, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Branch: "develop", Ref: "abcdef0123456789abcdef0123456789abcdef01"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if v := got.Spec.BuildEnv["DATABASE_URL"]; v != BuildEnvSecretRef("alpha-db-staging-conn", "url") {
		t.Errorf("staging build baked DATABASE_URL=%q, want the staging clone ref", v)
	}
	prod, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Branch: "main", Ref: "1111110123456789abcdef0123456789abcdef01"})
	if err != nil {
		t.Fatalf("Create prod: %v", err)
	}
	if v := prod.Spec.BuildEnv["DATABASE_URL"]; v != BuildEnvSecretRef("alpha-db-conn", "url") {
		t.Errorf("production build baked DATABASE_URL=%q, want the production ref", v)
	}
}

func TestCreate_ExplicitEnvUsesItsBranchAndVars(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedServiceWithSecretEnv("alpha", "web", "alpha-db-conn", "url"),
		seedProductionEnv("alpha", "web"),
		seedStagingEnv("alpha", "web"),
	)
	ctx := context.Background()
	mkSecret(t, s, "alpha-db-conn", "url", "postgres://PROD/db")
	mkSecret(t, s, "alpha-db-staging-conn", "url", "postgres://STAGING/db")
	got, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Env: "staging"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Spec.Branch != "develop" {
		t.Errorf("branch = %q, want the staging env's develop", got.Spec.Branch)
	}
	if v := got.Spec.BuildEnv["DATABASE_URL"]; v != BuildEnvSecretRef("alpha-db-staging-conn", "url") {
		t.Errorf("DATABASE_URL=%q, want the staging clone ref", v)
	}
	if _, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Env: "staging", Branch: "main"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("env + conflicting branch: want ErrInvalid, got %v", err)
	}
	if _, err := s.Create(ctx, "alpha", "web", CreateBuildRequest{Env: "qa"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown env: want ErrNotFound, got %v", err)
	}
}
