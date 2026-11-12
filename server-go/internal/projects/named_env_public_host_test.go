package projects

import (
	"context"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

// ISO-4 (2026-11-12): ${{ svc.PUBLIC_URL }} resolves against production, so
// a named env (staging) must have it rescoped to the sibling's staging host,
// both on service-level propagation and on a per-env override. A sibling
// with no staging env keeps production's host.
func TestNamedEnv_PublicURLRefsTargetSameScopeSibling(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "docs", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithHost("alpha", "web", "production", "alpha-web-production", "web.example.com"),
		seedEnvWithHost("alpha", "api", "production", "alpha-api-production", "api.example.com"),
		seedEnvWithHost("alpha", "docs", "production", "alpha-docs-production", "docs.example.com"),
		seedEnvWithHost("alpha", "web", "staging", "alpha-web-staging", "web-staging.example.com"),
		seedEnvWithHost("alpha", "api", "staging", "alpha-api-staging", "api-staging.example.com"),
	)
	ctx := context.Background()
	if err := s.SetEnv(ctx, "alpha", "web", []EnvVar{
		{Name: "API_URL", Value: "${{ api.PUBLIC_URL }}"},
		{Name: "DOCS_URL", Value: "${{ docs.PUBLIC_URL }}"},
	}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	if got := envCRValue(t, s, "alpha-web-production", "API_URL"); !strings.Contains(got, "//api.example.com") {
		t.Errorf("production API_URL = %q, want api.example.com", got)
	}
	if got := envCRValue(t, s, "alpha-web-staging", "API_URL"); !strings.Contains(got, "//api-staging.example.com") {
		t.Errorf("staging API_URL = %q, want api-staging.example.com", got)
	}
	if got := envCRValue(t, s, "alpha-web-staging", "DOCS_URL"); !strings.Contains(got, "//docs.example.com") {
		t.Errorf("staging DOCS_URL = %q, want production docs (no staging docs env)", got)
	}

	if _, err := s.SetEnvScopedVar(ctx, "alpha", "web", "staging", "OVERRIDE_URL", SetEnvVarRequest{Value: "${{ api.PUBLIC_URL }}"}); err != nil {
		t.Fatalf("SetEnvScopedVar: %v", err)
	}
	if got := envCRValue(t, s, "alpha-web-staging", "OVERRIDE_URL"); !strings.Contains(got, "//api-staging.example.com") {
		t.Errorf("staging override = %q, want api-staging.example.com", got)
	}
}

// AddEnvironment seeds the new env from the service's resolved vars, so it
// rescopes public hosts too, including the service's own.
func TestAddEnvironment_PublicURLRefsTargetSameScopeSibling(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "example.com"}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithHost("alpha", "web", "production", "alpha-web-production", "web.example.com"),
		seedEnvWithHost("alpha", "api", "production", "alpha-api-production", "api.example.com"),
		seedEnvWithHost("alpha", "api", "qa", "alpha-api-qa", "api-qa.example.com"),
	)
	ctx := context.Background()
	if err := s.SetEnv(ctx, "alpha", "web", []EnvVar{
		{Name: "API_URL", Value: "${{ api.PUBLIC_URL }}"},
		{Name: "SELF_URL", Value: "${{ web.PUBLIC_URL }}"},
	}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	env, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "qa", Branch: "qa", ShareAddons: true})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	if got := envCRValue(t, s, env.Name, "API_URL"); !strings.Contains(got, "//api-qa.example.com") {
		t.Errorf("qa API_URL = %q, want api-qa.example.com", got)
	}
	if got := envCRValue(t, s, env.Name, "SELF_URL"); strings.Contains(got, "//web.example.com") || !strings.Contains(got, "//"+env.Spec.Host) {
		t.Errorf("qa SELF_URL = %q, want the qa env's own host %q", got, env.Spec.Host)
	}
}
