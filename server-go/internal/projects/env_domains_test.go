package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

func TestAddEnvDomain_AppendsAndComputesTLS(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "staging", "develop", "alpha-web-staging"),
	)

	env, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com")
	if err != nil {
		t.Fatalf("AddEnvDomain: %v", err)
	}
	if !containsHost(env.Spec.AdditionalHosts, "staging.example.com") {
		t.Errorf("AdditionalHosts should contain the host, got %v", env.Spec.AdditionalHosts)
	}
	if !containsHost(env.Spec.TLSHosts, "staging.example.com") {
		t.Errorf("TLSHosts should contain the public FQDN, got %v", env.Spec.TLSHosts)
	}

	// Idempotent re-add.
	env, err = s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com")
	if err != nil {
		t.Fatalf("AddEnvDomain (re-add): %v", err)
	}
	n := 0
	for _, h := range env.Spec.AdditionalHosts {
		if h == "staging.example.com" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("re-add should be idempotent, host appears %d times", n)
	}
}

func TestAddEnvDomain_CrossEnvConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "develop", "alpha-web-staging"),
	)

	// Claim the host on production first.
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "production", "shop.example.com"); err != nil {
		t.Fatalf("seed production domain: %v", err)
	}
	// Staging trying to claim the same host must conflict.
	_, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "shop.example.com")
	if !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict when two envs claim the same host, got %v", err)
	}
}

func TestRemoveEnvDomain_Idempotent(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "staging", "develop", "alpha-web-staging"),
	)
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com"); err != nil {
		t.Fatalf("AddEnvDomain: %v", err)
	}
	env, err := s.RemoveEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com")
	if err != nil {
		t.Fatalf("RemoveEnvDomain: %v", err)
	}
	if containsHost(env.Spec.AdditionalHosts, "staging.example.com") || containsHost(env.Spec.TLSHosts, "staging.example.com") {
		t.Errorf("host should be removed from both lists, got additional=%v tls=%v", env.Spec.AdditionalHosts, env.Spec.TLSHosts)
	}
	// Removing an absent host is a no-op (no error).
	if _, err := s.RemoveEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com"); err != nil {
		t.Errorf("removing an absent host should be a no-op, got %v", err)
	}
}

func TestSetEnvDomains_NormalizesAndDedupes(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "staging", "develop", "alpha-web-staging"),
	)
	env, err := s.SetEnvDomains(context.Background(), "alpha", "web", "staging",
		[]string{"A.Example.com", "a.example.com", "  b.example.com  ", ""})
	if err != nil {
		t.Fatalf("SetEnvDomains: %v", err)
	}
	// "A.Example.com" + "a.example.com" dedupe to one lowercased entry; the
	// empty string is dropped; "b.example.com" trimmed.
	if len(env.Spec.AdditionalHosts) != 2 {
		t.Fatalf("expected 2 normalized hosts, got %v", env.Spec.AdditionalHosts)
	}
	if !containsHost(env.Spec.AdditionalHosts, "a.example.com") || !containsHost(env.Spec.AdditionalHosts, "b.example.com") {
		t.Errorf("normalized hosts wrong: %v", env.Spec.AdditionalHosts)
	}
}
