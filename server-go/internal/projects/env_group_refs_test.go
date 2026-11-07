package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedGroupService(project, short, group, source string) seed {
	labels := map[string]string{labelProject: project, labelService: short}
	var ann map[string]string
	if group != "" {
		labels[labelEnv] = group
		ann = map[string]string{"kuso.sislelabs.com/env-group-source-service": project + "-" + source}
	}
	name := project + "-" + short
	return typedSeed(kube.GVRServices, "KusoService", name, &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels, Annotations: ann},
		Spec:       kube.KusoServiceSpec{Project: project, Port: 3000},
	})
}

// seedGroupEnv seeds the single env of a service: "<fqn>-production" with
// label env=group (clones keep spec.kind=production).
func seedGroupEnv(project, short, group, host string, envFrom ...string) seed {
	name := project + "-" + short + "-production"
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{
			labelProject: project, labelService: short, labelEnv: group,
		}},
		Spec: kube.KusoEnvironmentSpec{
			Project: project, Service: project + "-" + short, Kind: "production",
			Host: host, TLSEnabled: host != "", EnvFromSecrets: envFrom,
		},
	})
}

func seedGroupAddon(project, short, kind, group, source string) seed {
	labels := map[string]string{labelProject: project}
	var ann map[string]string
	if group != "" {
		labels[labelEnv] = group
		ann = map[string]string{"kuso.sislelabs.com/env-group-source-addon": project + "-" + source}
	}
	name := project + "-" + short
	return typedSeed(kube.GVRAddons, "KusoAddon", name, &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: labels, Annotations: ann},
		Spec:       kube.KusoAddonSpec{Project: project, Kind: kind},
	})
}

// acme: api + web + worker in production; a staging group with copies of
// api and web only. withAddons adds db (cloned for staging) + cache (not).
func envGroupFixture(t *testing.T, withAddons bool) *Service {
	t.Helper()
	seeds := []seed{
		seedProject("acme", kube.KusoProjectSpec{}),
		seedGroupService("acme", "api", "", ""),
		seedGroupService("acme", "web", "", ""),
		seedGroupService("acme", "worker", "", ""),
		seedGroupService("acme", "api-staging", "staging", "api"),
		seedGroupService("acme", "web-staging", "staging", "web"),
		seedGroupEnv("acme", "api", "production", "api.example.com"),
		seedGroupEnv("acme", "web", "production", "web.example.com"),
		seedGroupEnv("acme", "worker", "production", ""),
		seedGroupEnv("acme", "api-staging", "staging", "api-staging.example.com"),
		seedGroupEnv("acme", "web-staging", "staging", "web-staging.example.com", "acme-db-staging-conn"),
	}
	if withAddons {
		seeds = append(seeds,
			seedGroupAddon("acme", "db", "postgres", "", ""),
			seedGroupAddon("acme", "db-staging", "postgres", "staging", "db"),
			seedGroupAddon("acme", "cache", "redis", "", ""),
		)
	}
	s := fakeService(t, seeds...)
	conns := []string{"acme-db-conn", "acme-db-staging-conn", "acme-cache-conn"}
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) { return conns, nil }
	s.ReferenceableConnSecrets = s.AddonConnSecrets
	return s
}

func svcEnvValue(t *testing.T, s *Service, svc, name string) string {
	t.Helper()
	got, err := s.Kube.GetKusoService(context.Background(), "kuso", svc)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Spec.EnvVars {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("%s has no %s: %+v", svc, name, got.Spec.EnvVars)
	return ""
}

func envCRValue(t *testing.T, s *Service, env, name string) string {
	t.Helper()
	got, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", env)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range got.Spec.EnvVars {
		if e.Name == name {
			return e.Value
		}
	}
	t.Fatalf("%s has no %s: %+v", env, name, got.Spec.EnvVars)
	return ""
}

func TestSetEnv_GroupCloneRefResolvesToGroupSibling(t *testing.T) {
	t.Parallel()
	s := envGroupFixture(t, false)
	err := s.SetEnv(context.Background(), "acme", "web-staging", []EnvVar{
		{Name: "API_URL", Value: "${{ api.URL }}"},
		{Name: "API_PUBLIC", Value: "${{ api.PUBLIC_URL }}"},
	})
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	const wantURL = "http://acme-api-staging-production.kuso.svc.cluster.local"
	if got := svcEnvValue(t, s, "acme-web-staging", "API_URL"); got != wantURL {
		t.Errorf("service API_URL = %q, want %q", got, wantURL)
	}
	if got := svcEnvValue(t, s, "acme-web-staging", "API_PUBLIC"); got != "https://api-staging.example.com" {
		t.Errorf("service API_PUBLIC = %q, want the staging host", got)
	}
	// Propagation must not rescope it on the clone's -production env.
	if got := envCRValue(t, s, "acme-web-staging-production", "API_URL"); got != wantURL {
		t.Errorf("env API_URL = %q, want %q", got, wantURL)
	}
}

func TestSetEnvScopedVar_GroupCloneRefResolvesToGroupSibling(t *testing.T) {
	t.Parallel()
	s := envGroupFixture(t, false)
	if _, err := s.SetEnvScopedVar(context.Background(), "acme", "web-staging", "production", "API_URL", SetEnvVarRequest{Value: "${{ api.URL }}"}); err != nil {
		t.Fatalf("SetEnvScopedVar: %v", err)
	}
	if got := envCRValue(t, s, "acme-web-staging-production", "API_URL"); got != "http://acme-api-staging-production.kuso.svc.cluster.local" {
		t.Errorf("env API_URL = %q, want the staging sibling", got)
	}
}

func TestSetEnv_ProductionRefUnchanged(t *testing.T) {
	t.Parallel()
	s := envGroupFixture(t, false)
	err := s.SetEnv(context.Background(), "acme", "web", []EnvVar{
		{Name: "API_URL", Value: "${{ api.URL }}"},
		{Name: "API_PUBLIC", Value: "${{ api.PUBLIC_URL }}"},
	})
	if err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
	if got := svcEnvValue(t, s, "acme-web", "API_URL"); got != "http://acme-api-production.kuso.svc.cluster.local" {
		t.Errorf("API_URL = %q", got)
	}
	if got := svcEnvValue(t, s, "acme-web", "API_PUBLIC"); got != "https://api.example.com" {
		t.Errorf("API_PUBLIC = %q", got)
	}
}

func TestSetEnv_GroupCloneRefWithoutSiblingRejected(t *testing.T) {
	t.Parallel()
	s := envGroupFixture(t, false)
	err := s.SetEnv(context.Background(), "acme", "web-staging", []EnvVar{{Name: "W", Value: "${{ worker.URL }}"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SetEnv err = %v, want ErrConflict (worker has no staging copy)", err)
	}
	_, err = s.SetEnvScopedVar(context.Background(), "acme", "web-staging", "production", "W", SetEnvVarRequest{Value: "${{ worker.URL }}"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SetEnvScopedVar err = %v, want ErrConflict", err)
	}
}

func TestSetEnv_IsolatedGroupRefToUnclonedAddonRejected(t *testing.T) {
	t.Parallel()
	s := envGroupFixture(t, true)
	// db has a staging clone: allowed (propagation re-scopes it).
	if err := s.SetEnv(context.Background(), "acme", "web-staging", []EnvVar{{Name: "DATABASE_URL", Value: "${{ db.DATABASE_URL }}"}}); err != nil {
		t.Fatalf("cloned addon ref: %v", err)
	}
	err := s.SetEnv(context.Background(), "acme", "web-staging", []EnvVar{{Name: "REDIS_URL", Value: "${{ cache.REDIS_URL }}"}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SetEnv err = %v, want ErrConflict (staging has no cache copy)", err)
	}
	_, err = s.SetEnvScopedVar(context.Background(), "acme", "web-staging", "production", "REDIS_URL", SetEnvVarRequest{Value: "${{ cache.REDIS_URL }}"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("SetEnvScopedVar err = %v, want ErrConflict", err)
	}
	// Production is never restricted.
	if err := s.SetEnv(context.Background(), "acme", "web", []EnvVar{{Name: "REDIS_URL", Value: "${{ cache.REDIS_URL }}"}}); err != nil {
		t.Fatalf("production ref: %v", err)
	}
}

// A group with no addon copies shares production by design.
func TestSetEnv_SharedGroupRefToProductionAddonAllowed(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{}),
		seedGroupService("acme", "web", "", ""),
		seedGroupService("acme", "web-staging", "staging", "web"),
		seedGroupEnv("acme", "web-staging", "staging", ""),
		seedGroupAddon("acme", "cache", "redis", "", ""),
	)
	s.AddonConnSecrets = func(context.Context, string) ([]string, error) { return []string{"acme-cache-conn"}, nil }
	if err := s.SetEnv(context.Background(), "acme", "web-staging", []EnvVar{{Name: "REDIS_URL", Value: "${{ cache.REDIS_URL }}"}}); err != nil {
		t.Fatalf("SetEnv: %v", err)
	}
}
