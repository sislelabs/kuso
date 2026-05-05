package projects

import (
	"context"
	"errors"
	"sync"
	"testing"

	"kuso/server/internal/kube"
)

func TestAddDomain_AppendsToEmptyList(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{
		Host: "api.example.com",
		TLS:  true,
	})
	if err != nil {
		t.Fatalf("AddDomain: %v", err)
	}
	if len(got.Spec.Domains) != 1 || got.Spec.Domains[0].Host != "api.example.com" || !got.Spec.Domains[0].TLS {
		t.Fatalf("Domains: %+v", got.Spec.Domains)
	}
}

func TestAddDomain_DuplicateReturnsConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			Domains: []kube.KusoDomain{{Host: "api.example.com", TLS: true}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	_, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{
		Host: "api.example.com",
		TLS:  true,
	})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("want ErrConflict on dup, got %v", err)
	}
}

func TestAddDomain_DuplicateWithDifferentTLSFlipsFlag(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			Domains: []kube.KusoDomain{{Host: "api.example.com", TLS: false}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{
		Host: "api.example.com",
		TLS:  true,
	})
	if err != nil {
		t.Fatalf("AddDomain (flip TLS): %v", err)
	}
	if len(got.Spec.Domains) != 1 || !got.Spec.Domains[0].TLS {
		t.Errorf("expected single domain with TLS=true, got %+v", got.Spec.Domains)
	}
}

func TestAddDomain_RejectsInvalidHostname(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	cases := []string{"", "  ", "-leading.dash.com", "trailing.dash-.com", "double..dot.com", ".leading.dot"}
	for _, h := range cases {
		_, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{Host: h, TLS: true})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("AddDomain(%q): want ErrInvalid, got %v", h, err)
		}
	}
}

func TestRemoveDomain_DropsByHostName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			Domains: []kube.KusoDomain{
				{Host: "api.example.com", TLS: true},
				{Host: "alt.example.com", TLS: true},
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.RemoveDomain(context.Background(), "alpha", "web", "api.example.com")
	if err != nil {
		t.Fatalf("RemoveDomain: %v", err)
	}
	if len(got.Spec.Domains) != 1 || got.Spec.Domains[0].Host != "alt.example.com" {
		t.Errorf("Domains after remove: %+v", got.Spec.Domains)
	}
}

func TestRemoveDomain_NotFoundIsErr(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	_, err := s.RemoveDomain(context.Background(), "alpha", "web", "nope.example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestAddDomain_ConcurrentAddsAllSurvive is the meat of why these
// delta operations exist. Two goroutines fire AddDomain at the same
// service simultaneously; both edits must be present afterward. With
// PUT-the-whole-spec semantics one would silently stomp the other —
// the per-service mutex inside AddDomain is what prevents that.
func TestAddDomain_ConcurrentAddsAllSurvive(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	hosts := []string{
		"a.example.com", "b.example.com", "c.example.com", "d.example.com", "e.example.com",
		"f.example.com", "g.example.com", "h.example.com", "i.example.com", "j.example.com",
	}

	var wg sync.WaitGroup
	wg.Add(len(hosts))
	errs := make(chan error, len(hosts))
	for _, h := range hosts {
		h := h
		go func() {
			defer wg.Done()
			if _, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{Host: h, TLS: true}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent AddDomain: %v", err)
	}

	got, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if len(got.Spec.Domains) != len(hosts) {
		t.Fatalf("lost concurrent edits: have %d domains, expected %d (%+v)",
			len(got.Spec.Domains), len(hosts), got.Spec.Domains)
	}
	have := map[string]bool{}
	for _, d := range got.Spec.Domains {
		have[d.Host] = true
	}
	for _, h := range hosts {
		if !have[h] {
			t.Errorf("missing %q after concurrent add", h)
		}
	}
}

func TestSetEnvVar_AddsLiteralValue(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.SetEnvVar(context.Background(), "alpha", "web", "FOO", SetEnvVarRequest{Value: "bar"})
	if err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	if len(got.Spec.EnvVars) != 1 || got.Spec.EnvVars[0].Name != "FOO" || got.Spec.EnvVars[0].Value != "bar" {
		t.Errorf("EnvVars: %+v", got.Spec.EnvVars)
	}
}

func TestSetEnvVar_OverwritesExisting(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			EnvVars: []kube.KusoEnvVar{{Name: "FOO", Value: "old"}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.SetEnvVar(context.Background(), "alpha", "web", "FOO", SetEnvVarRequest{Value: "new"})
	if err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	if len(got.Spec.EnvVars) != 1 || got.Spec.EnvVars[0].Value != "new" {
		t.Errorf("expected single FOO=new, got %+v", got.Spec.EnvVars)
	}
}

func TestSetEnvVar_RejectsBothValueAndSecretRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	_, err := s.SetEnvVar(context.Background(), "alpha", "web", "DB", SetEnvVarRequest{
		Value:     "literal",
		SecretRef: &SetEnvVarSecretRefBody{Name: "s", Key: "k"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid for both, got %v", err)
	}

	_, err = s.SetEnvVar(context.Background(), "alpha", "web", "DB", SetEnvVarRequest{})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid for neither, got %v", err)
	}
}

func TestSetEnvVar_AcceptsSecretRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.SetEnvVar(context.Background(), "alpha", "web", "DATABASE_URL", SetEnvVarRequest{
		SecretRef: &SetEnvVarSecretRefBody{Name: "alpha-postgres-conn", Key: "DATABASE_URL"},
	})
	if err != nil {
		t.Fatalf("SetEnvVar: %v", err)
	}
	if len(got.Spec.EnvVars) != 1 {
		t.Fatalf("EnvVars: %+v", got.Spec.EnvVars)
	}
	ref := got.Spec.EnvVars[0].ValueFrom
	if ref == nil {
		t.Fatal("ValueFrom is nil")
	}
	skr, ok := ref["secretKeyRef"].(map[string]any)
	if !ok {
		t.Fatalf("secretKeyRef shape: %T", ref["secretKeyRef"])
	}
	if skr["name"] != "alpha-postgres-conn" || skr["key"] != "DATABASE_URL" {
		t.Errorf("secretKeyRef: %+v", skr)
	}
}

func TestUnsetEnvVar_RemovesByName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Project: "alpha", Port: 8080,
			EnvVars: []kube.KusoEnvVar{
				{Name: "FOO", Value: "1"},
				{Name: "BAR", Value: "2"},
			},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.UnsetEnvVar(context.Background(), "alpha", "web", "FOO")
	if err != nil {
		t.Fatalf("UnsetEnvVar: %v", err)
	}
	if len(got.Spec.EnvVars) != 1 || got.Spec.EnvVars[0].Name != "BAR" {
		t.Errorf("after unset: %+v", got.Spec.EnvVars)
	}
}

// TestSetEnvVar_ConcurrentDifferentKeysAllSurvive is the env-var
// analogue of the concurrent-domains test. N goroutines set N
// distinct keys; the per-service mutex must serialise them so all N
// land. Without the mutex, last-write-wins drops most of them.
func TestSetEnvVar_ConcurrentDifferentKeysAllSurvive(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	keys := []string{"K0", "K1", "K2", "K3", "K4", "K5", "K6", "K7", "K8", "K9"}
	var wg sync.WaitGroup
	wg.Add(len(keys))
	errs := make(chan error, len(keys))
	for _, k := range keys {
		k := k
		go func() {
			defer wg.Done()
			if _, err := s.SetEnvVar(context.Background(), "alpha", "web", k, SetEnvVarRequest{Value: "v"}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent SetEnvVar: %v", err)
	}

	got, err := s.GetService(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("GetService: %v", err)
	}
	if len(got.Spec.EnvVars) != len(keys) {
		t.Fatalf("lost concurrent env-var edits: have %d, expected %d", len(got.Spec.EnvVars), len(keys))
	}
}
