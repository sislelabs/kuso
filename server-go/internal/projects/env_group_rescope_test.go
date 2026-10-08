package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// Env-group clones' env CRs end in -production, so their in-cluster
// sibling URLs are already right after CreateEnvGroup. Propagation must not
// rescope them to -<group> (a Service that doesn't exist).
func TestPropagate_EnvGroupCloneKeepsSiblingURL(t *testing.T) {
	t.Parallel()
	const apiURL = "http://alpha-api-production.kuso.svc.cluster.local:8080"
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "api", "production", "main", "alpha-api-production"),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 3000,
			EnvVars: []kube.KusoEnvVar{{Name: "API_URL", Value: apiURL}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "alpha", CreateEnvGroupRequest{Name: "qa"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	const want = "http://alpha-api-qa-production.kuso.svc.cluster.local:8080"
	if got := envCRVarValue(t, s, "alpha-web-qa-production", "API_URL"); got != want {
		t.Fatalf("API_URL after create = %q, want %q", got, want)
	}
	if _, err := s.SetEnvVar(ctx, "alpha", "web-qa", "FOO", SetEnvVarRequest{Value: "bar"}); err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	if got := envCRVarValue(t, s, "alpha-web-qa-production", "API_URL"); got != want {
		t.Errorf("API_URL after unrelated env edit = %q, want %q", got, want)
	}
}

func envCRVarValue(t *testing.T, s *Service, envCR, name string) string {
	t.Helper()
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", envCR)
	if err != nil {
		t.Fatalf("get env %s: %v", envCR, err)
	}
	for _, v := range env.Spec.EnvVars {
		if v.Name == name {
			return v.Value
		}
	}
	return ""
}
