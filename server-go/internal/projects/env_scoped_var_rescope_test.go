package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// `kuso env set --env staging X='${{ db.DATABASE_URL }}'` resolved against
// production and pinned the result as an override that propagation never
// re-scopes, so staging pods read the production database forever.
func TestSetEnvScopedVar_ScopesRefsToTheTargetEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000}),
		seedEnv("alpha", "api", "staging", "dev", "alpha-api-staging"),
		seedScopedAddon("alpha", "alpha-db-staging", map[string]string{labelEnv: "staging"}, ""),
	)
	s.ReferenceableConnSecrets = func(context.Context, string) ([]string, error) {
		return []string{"alpha-db-conn", "alpha-db-staging-conn"}, nil
	}
	ctx := context.Background()

	if _, err := s.SetEnvScopedVar(ctx, "alpha", "api", "staging", "DATABASE_URL", SetEnvVarRequest{Value: "${{ db.DATABASE_URL }}"}); err != nil {
		t.Fatalf("set DATABASE_URL: %v", err)
	}
	env, err := s.SetEnvScopedVar(ctx, "alpha", "api", "staging", "WEB_URL", SetEnvVarRequest{Value: "${{ web.URL }}"})
	if err != nil {
		t.Fatalf("set WEB_URL: %v", err)
	}
	got := map[string]kube.KusoEnvVar{}
	for _, e := range env.Spec.EnvVars {
		got[e.Name] = e
	}
	skr, _ := got["DATABASE_URL"].ValueFrom["secretKeyRef"].(map[string]any)
	if skr["name"] != "alpha-db-staging-conn" {
		t.Errorf("DATABASE_URL secretKeyRef = %v, want alpha-db-staging-conn", skr["name"])
	}
	if want := "http://alpha-web-staging.kuso.svc.cluster.local"; got["WEB_URL"].Value != want {
		t.Errorf("WEB_URL = %q, want %q", got["WEB_URL"].Value, want)
	}
}
