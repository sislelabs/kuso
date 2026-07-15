package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// envVarValue finds an env var by name in an env CR, returning its literal
// Value (empty if absent or valueFrom-backed).
func envVarValue(env *kube.KusoEnvironment, name string) (string, bool) {
	for _, e := range env.Spec.EnvVars {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

// TestAddEnvironment_RescopesServiceRefLiterals is the regression test for
// the staging-API_URL bug: a new env must NOT inherit the production-scoped
// in-cluster service URL. AddEnvironment rescopes <svc>-production refs to
// <svc>-<env>.
func TestAddEnvironment_RescopesServiceRefLiterals(t *testing.T) {
	t.Parallel()
	// Service whose API_URL was resolved against production (the shape
	// SetEnv produces from ${{ api.URL }}).
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha",
			EnvVars: []kube.KusoEnvVar{
				{Name: "API_URL", Value: "http://alpha-api-production.kuso.svc.cluster.local"},
				{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"},
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	env, err := s.AddEnvironment(context.Background(), "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "stage"})
	if err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	got, _ := envVarValue(env, "API_URL")
	if got != "http://alpha-api-staging.kuso.svc.cluster.local" {
		t.Errorf("staging API_URL should be rescoped to the staging api service, got %q", got)
	}
}

// TestSetEnvScopedVar_OverridesAndUnsets covers the per-env override write
// path: set an override on one env, upsert it, then unset it.
func TestSetEnvScopedVar_OverridesAndUnsets(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha",
			EnvVars: []kube.KusoEnvVar{{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"}}}),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	env, err := s.SetEnvScopedVar(context.Background(), "alpha", "web", "staging",
		"NEXT_PUBLIC_ENVIRONMENT", SetEnvVarRequest{Value: "staging"})
	if err != nil {
		t.Fatalf("SetEnvScopedVar: %v", err)
	}
	if v, _ := envVarValue(env, "NEXT_PUBLIC_ENVIRONMENT"); v != "staging" {
		t.Errorf("override should be staging, got %q", v)
	}
	// Upsert (no duplicate).
	env, _ = s.SetEnvScopedVar(context.Background(), "alpha", "web", "staging",
		"NEXT_PUBLIC_ENVIRONMENT", SetEnvVarRequest{Value: "staging2"})
	n := 0
	for _, e := range env.Spec.EnvVars {
		if e.Name == "NEXT_PUBLIC_ENVIRONMENT" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("upsert should not duplicate, found %d", n)
	}
	// Unset.
	env, err = s.UnsetEnvScopedVar(context.Background(), "alpha", "web", "staging", "NEXT_PUBLIC_ENVIRONMENT")
	if err != nil {
		t.Fatalf("UnsetEnvScopedVar: %v", err)
	}
	if _, ok := envVarValue(env, "NEXT_PUBLIC_ENVIRONMENT"); ok {
		t.Error("override should be removed after unset")
	}
	// Unset absent → ErrNotFound.
	if _, err := s.UnsetEnvScopedVar(context.Background(), "alpha", "web", "staging", "NOPE"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unset absent should be ErrNotFound, got %v", err)
	}
}

// TestSetEnvScopedVar_SurvivesServicePropagation is the crux: a per-env
// override must survive a subsequent service-level env write (the merge
// layers env over service, env wins). Without this the override would be
// flattened back to the service value on the next `env set`.
func TestSetEnvScopedVar_SurvivesServicePropagation(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha",
			EnvVars: []kube.KusoEnvVar{{Name: "NEXT_PUBLIC_ENVIRONMENT", Value: "production"}}}),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	// Set the staging override.
	if _, err := s.SetEnvScopedVar(context.Background(), "alpha", "web", "staging",
		"NEXT_PUBLIC_ENVIRONMENT", SetEnvVarRequest{Value: "staging"}); err != nil {
		t.Fatalf("SetEnvScopedVar: %v", err)
	}
	// Now do a service-level env write (triggers propagateChangedToEnvs).
	if _, err := s.SetEnvVar(context.Background(), "alpha", "web", "SOMETHING_ELSE",
		SetEnvVarRequest{Value: "x"}); err != nil {
		t.Fatalf("service SetEnvVar: %v", err)
	}
	// The staging env's override must still be "staging", not flattened.
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-staging")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if v, _ := envVarValue(env, "NEXT_PUBLIC_ENVIRONMENT"); v != "staging" {
		t.Errorf("override must survive service propagation, got %q (flattened to service value?)", v)
	}
}

// TestSetEnvScopedVar_ResolvesRefAtSetTime is the regression test for the
// env-scoped-ref bug: `kuso env set --env staging 'X=${{ api.URL }}'` must
// resolve the ref to a concrete in-cluster URL at set time (like the
// service-level path), NOT store the raw `${{ }}` literal. Storing it raw
// meant the pod got the literal string AND the next service-level
// propagation silently dropped the override as an "unresolved ref".
func TestSetEnvScopedVar_ResolvesRefAtSetTime(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	env, err := s.SetEnvScopedVar(context.Background(), "alpha", "web", "staging",
		"API_URL", SetEnvVarRequest{Value: "${{ api.URL }}"})
	if err != nil {
		t.Fatalf("SetEnvScopedVar: %v", err)
	}
	got, _ := envVarValue(env, "API_URL")
	if got == "${{ api.URL }}" || got == "" {
		t.Fatalf("env-scoped ref should resolve at set time, got %q (raw literal = bug)", got)
	}
	// Resolved to a concrete in-cluster URL (same shape the service-level
	// SetEnv path produces). The point of the fix is that it is RESOLVED,
	// not raw — so it both works in the pod and survives propagation.
	const want = "http://alpha-api-production.kuso.svc.cluster.local"
	if got != want {
		t.Errorf("resolved ref = %q, want %q", got, want)
	}

	// And it must survive a subsequent service-level write — the path that
	// previously dropped it as an "unresolved ref" and re-stamped the
	// service value.
	if _, err := s.SetEnvVar(context.Background(), "alpha", "web", "SOMETHING_ELSE",
		SetEnvVarRequest{Value: "x"}); err != nil {
		t.Fatalf("service SetEnvVar: %v", err)
	}
	after, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-staging")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if v, _ := envVarValue(after, "API_URL"); v != want {
		t.Errorf("resolved override must survive propagation, got %q", v)
	}
}

func TestAddEnvDomain_AppendsAndComputesTLS(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "web", "staging", "develop", "alpha-web-staging"),
	)

	env, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com", "")
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
	env, err = s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com", "")
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
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "production", "shop.example.com", ""); err != nil {
		t.Fatalf("seed production domain: %v", err)
	}
	// Staging trying to claim the same host must conflict.
	_, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "shop.example.com", "")
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
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "staging", "staging.example.com", ""); err != nil {
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

// Wildcard hosts land in env.Spec.WildcardDomains with their cert secret —
// never in AdditionalHosts/TLSHosts (that would mint per-host LE certs for
// hosts the wildcard already covers). tlsSecret is mandatory: a wildcard
// can't be HTTP-01'd, so routing one without a pre-provisioned cert would
// serve traefik's default cert and read as an outage.
func TestAddEnvDomain_Wildcard(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x", DefaultBranch: "main"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 3000}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	// Missing tlsSecret → rejected.
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "production", "*.example.com", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("wildcard without tlsSecret should be ErrInvalid, got %v", err)
	}

	env, err := s.AddEnvDomain(context.Background(), "alpha", "web", "production", "*.example.com", "wildcard-example-tls")
	if err != nil {
		t.Fatalf("AddEnvDomain wildcard: %v", err)
	}
	if len(env.Spec.WildcardDomains) != 1 ||
		env.Spec.WildcardDomains[0].Host != "*.example.com" ||
		env.Spec.WildcardDomains[0].TLSSecret != "wildcard-example-tls" {
		t.Fatalf("wildcardDomains wrong: %+v", env.Spec.WildcardDomains)
	}
	if len(env.Spec.AdditionalHosts) != 0 {
		t.Errorf("wildcard must not land in additionalHosts: %v", env.Spec.AdditionalHosts)
	}
	for _, h := range env.Spec.TLSHosts {
		if h == "*.example.com" {
			t.Errorf("wildcard must not land in tlsHosts (per-host LE issuance): %v", env.Spec.TLSHosts)
		}
	}

	// Idempotent upsert: re-add with a new secret updates in place.
	env, err = s.AddEnvDomain(context.Background(), "alpha", "web", "production", "*.example.com", "rotated-tls")
	if err != nil {
		t.Fatalf("re-add: %v", err)
	}
	if len(env.Spec.WildcardDomains) != 1 || env.Spec.WildcardDomains[0].TLSSecret != "rotated-tls" {
		t.Fatalf("upsert should update the secret: %+v", env.Spec.WildcardDomains)
	}

	// tlsSecret on a NON-wildcard host is rejected (scope guard).
	if _, err := s.AddEnvDomain(context.Background(), "alpha", "web", "production", "shop.example.com", "some-tls"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tlsSecret on non-wildcard should be ErrInvalid, got %v", err)
	}

	// Removal via the same verb.
	env, err = s.RemoveEnvDomain(context.Background(), "alpha", "web", "production", "*.example.com")
	if err != nil {
		t.Fatalf("RemoveEnvDomain wildcard: %v", err)
	}
	if len(env.Spec.WildcardDomains) != 0 {
		t.Fatalf("wildcard should be removed: %+v", env.Spec.WildcardDomains)
	}
}
