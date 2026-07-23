package projects

import (
	"context"
	"errors"
	"sync"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// fakeServiceWithSecrets mirrors fakeService but also wires a fake
// CoreV1 Clientset so the managed-secret (SecretValue) code paths, which
// read/write real Secrets, have a backing store. Any *corev1.Secret in
// secretObjs is pre-seeded.
func fakeServiceWithSecrets(t *testing.T, secretObjs []runtime.Object, seeds ...seed) *Service {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for _, sd := range seeds {
		if err := dyn.Tracker().Create(sd.gvr, sd.obj, sd.obj.GetNamespace()); err != nil {
			t.Fatalf("seed %s: %v", sd.obj.GetName(), err)
		}
	}
	cs := k8sfake.NewSimpleClientset(secretObjs...)
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")
}

// getSecret is a small test helper: fetch a Secret from the fake clientset.
func getSecret(t *testing.T, s *Service, ns, name string) *corev1.Secret {
	t.Helper()
	sec, err := s.Kube.Clientset.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret %s: %v", name, err)
	}
	return sec
}

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

// TestAddDomain_PropagatesToProductionEnv is the regression test for the
// custom-domain bug: `kuso domains add` must reach the production env's
// AdditionalHosts/TLSHosts (which is what the chart renders the Ingress +
// TLS cert from). A service-level spec.domains write alone is invisible to
// routing.
func TestAddDomain_PropagatesToProductionEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	if _, err := s.AddDomain(context.Background(), "alpha", "web", AddDomainRequest{
		Host: "shop.example.com",
		TLS:  true,
	}); err != nil {
		t.Fatalf("AddDomain: %v", err)
	}

	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !containsHost(env.Spec.AdditionalHosts, "shop.example.com") {
		t.Errorf("production env AdditionalHosts should contain the custom domain, got %v", env.Spec.AdditionalHosts)
	}
	// TLS=true + public FQDN → must be in TLSHosts so a cert is minted.
	if !containsHost(env.Spec.TLSHosts, "shop.example.com") {
		t.Errorf("production env TLSHosts should contain the custom domain, got %v", env.Spec.TLSHosts)
	}

	// Removing it must clean both lists.
	if _, err := s.RemoveDomain(context.Background(), "alpha", "web", "shop.example.com"); err != nil {
		t.Fatalf("RemoveDomain: %v", err)
	}
	env, _ = s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if containsHost(env.Spec.AdditionalHosts, "shop.example.com") || containsHost(env.Spec.TLSHosts, "shop.example.com") {
		t.Errorf("after RemoveDomain the production env should drop the host, got additional=%v tls=%v", env.Spec.AdditionalHosts, env.Spec.TLSHosts)
	}
}

func containsHost(hosts []string, h string) bool {
	for _, x := range hosts {
		if x == h {
			return true
		}
	}
	return false
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
	// The ref names an addon conn secret this project owns; validateSecretRefName
	// requires it to appear in AddonConnSecrets (cross-project theft guard).
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-postgres-conn"}, nil
	}

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

// TestSetEnvVar_RejectsForeignSecretRef locks in the cross-project
// credential-theft guard: project alpha may not reference another
// project's -conn secret, even though the CRD name-shape regex would
// accept it.
func TestSetEnvVar_RejectsForeignSecretRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	// alpha owns only its own conn secret; "beta-pg-conn" belongs to
	// another project and must be refused.
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	_, err := s.SetEnvVar(context.Background(), "alpha", "web", "STOLEN", SetEnvVarRequest{
		SecretRef: &SetEnvVarSecretRefBody{Name: "beta-pg-conn", Key: "PASSWORD"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for foreign secretRef, got %v", err)
	}
}

// TestSetEnvPending_RejectsForeignAddonRef closes the apply-path variant
// of the cross-project theft vector: `kuso apply` uses AllowPending, which
// rewrites an unresolved `${{ beta-pg.PASSWORD }}` into a speculative
// `beta-pg-conn` secretKeyRef — another project's real conn secret. The
// SetEnvWithOpts ownership guard must reject it.
func TestSetEnvPending_RejectsForeignAddonRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	// alpha owns only alpha-pg-conn.
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	// A pending ref to project beta's addon → speculative beta-pg-conn.
	err := s.SetEnvPending(context.Background(), "alpha", "web", []EnvVar{
		{Name: "STOLEN", Value: "${{ beta-pg.PASSWORD }}"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("apply-path foreign addon ref must be rejected, got %v", err)
	}
}

// TestSetEnvPending_AllowsOwnAddonRef proves the guard doesn't break the
// legitimate case: a pending ref to THIS project's declared addon passes.
func TestSetEnvPending_AllowsOwnAddonRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedAddon("alpha", "pg", "postgres"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	if err := s.SetEnvPending(context.Background(), "alpha", "web", []EnvVar{
		{Name: "DB", Value: "${{ pg.URL }}"},
	}); err != nil {
		t.Fatalf("own-project addon ref must be allowed, got %v", err)
	}
}

// TestSetEnvVar_AcceptsEnvScopedSecret proves the ownership guard doesn't
// reject a legitimate env-scoped secret (<P>-<SVC>-<env>-secrets), which is
// owned by this service — an earlier version only accepted the plain
// <P>-<SVC>-secrets form.
func TestSetEnvVar_AcceptsEnvScopedSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	s.AddonConnSecrets = func(ctx context.Context, project string) ([]string, error) {
		return []string{"alpha-pg-conn"}, nil
	}
	// alpha-web-staging-secrets is this service's env-scoped secret.
	if err := s.validateSecretRefName(context.Background(), "alpha", "web", "alpha-web-staging-secrets"); err != nil {
		t.Fatalf("env-scoped own secret must be allowed, got %v", err)
	}
	// But another service's secret must still be rejected.
	if err := s.validateSecretRefName(context.Background(), "alpha", "web", "alpha-api-secrets"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("another service's secret must be rejected, got %v", err)
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

func strptr(s string) *string { return &s }

// TestSetEnvVar_SecretValueUpsertsManagedSecret is the core new-mode test:
// SecretValue writes into <project>-<service>-secrets (creating it if
// absent), does NOT touch spec.envVars, and rolls the pods via a
// spec.secretsRev bump on the owned env.
func TestSetEnvVar_SecretValueCreatesManagedSecret(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	got, err := s.SetEnvVar(context.Background(), "alpha", "web", "WETRAVEL_API_KEY", SetEnvVarRequest{
		SecretValue: strptr("sk-live-123"),
	})
	if err != nil {
		t.Fatalf("SetEnvVar (secretValue): %v", err)
	}
	// spec.envVars must be untouched.
	if len(got.Spec.EnvVars) != 0 {
		t.Errorf("spec.envVars should be untouched, got %+v", got.Spec.EnvVars)
	}
	// The managed secret must now carry the key with the plaintext value.
	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if string(sec.Data["WETRAVEL_API_KEY"]) != "sk-live-123" {
		t.Errorf("secret value: %q", sec.Data["WETRAVEL_API_KEY"])
	}
	// Managed labels stamped on create.
	if sec.Labels[kube.ManagedByLabel] != "kuso-server" ||
		sec.Labels[kube.LabelProject] != "alpha" ||
		sec.Labels[kube.LabelService] != "web" {
		t.Errorf("managed labels missing/wrong: %+v", sec.Labels)
	}
	// Rollout: spec.secretsRev bumped on the production env.
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.SecretsRev == "" {
		t.Errorf("expected spec.secretsRev to be bumped on the env, got empty")
	}
}

// TestSetEnvVar_SecretValuePreservesOtherKeysAndAnnotations locks in the
// read-modify-write contract: writing one key must NOT drop other keys and
// must NOT clear the secrets.kuso.sislelabs.com/generated-* annotations.
func TestSetEnvVar_SecretValuePreservesOtherKeysAndAnnotations(t *testing.T) {
	t.Parallel()
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kube.ServiceSecretName("alpha", "web"),
			Namespace: "kuso",
			Labels: map[string]string{
				kube.ManagedByLabel: "kuso-server",
				kube.LabelProject:   "alpha",
				kube.LabelService:   "web",
			},
			Annotations: map[string]string{
				"secrets.kuso.sislelabs.com/generated-JWT_SECRET": "hex32",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"JWT_SECRET": []byte("deadbeef"),
			"OTHER_KEY":  []byte("keepme"),
		},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{existing},
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	if _, err := s.SetEnvVar(context.Background(), "alpha", "web", "WETRAVEL_API_KEY", SetEnvVarRequest{
		SecretValue: strptr("sk-live-123"),
	}); err != nil {
		t.Fatalf("SetEnvVar (secretValue): %v", err)
	}

	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if string(sec.Data["WETRAVEL_API_KEY"]) != "sk-live-123" {
		t.Errorf("new key value: %q", sec.Data["WETRAVEL_API_KEY"])
	}
	if string(sec.Data["JWT_SECRET"]) != "deadbeef" {
		t.Errorf("JWT_SECRET clobbered: %q", sec.Data["JWT_SECRET"])
	}
	if string(sec.Data["OTHER_KEY"]) != "keepme" {
		t.Errorf("OTHER_KEY clobbered: %q", sec.Data["OTHER_KEY"])
	}
	if sec.Annotations["secrets.kuso.sislelabs.com/generated-JWT_SECRET"] != "hex32" {
		t.Errorf("generated-* annotation cleared: %+v", sec.Annotations)
	}
}

// TestSetEnvVar_RejectsValuePlusSecretValue extends the XOR guard to the
// third mode — value + secretValue together is invalid.
func TestSetEnvVar_RejectsValuePlusSecretValue(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.SetEnvVar(context.Background(), "alpha", "web", "DB", SetEnvVarRequest{
		Value:       "literal",
		SecretValue: strptr("secret"),
	})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("want ErrInvalid for value+secretValue, got %v", err)
	}
}

// TestSetEnvVar_SecretValueEmptyStringIsValid proves a non-nil empty
// SecretValue counts as "set" (clearing a key's value while keeping it).
func TestSetEnvVar_SecretValueEmptyStringIsValid(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	if _, err := s.SetEnvVar(context.Background(), "alpha", "web", "EMPTY_OK", SetEnvVarRequest{
		SecretValue: strptr(""),
	}); err != nil {
		t.Fatalf("empty secretValue should be valid, got %v", err)
	}
	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if _, ok := sec.Data["EMPTY_OK"]; !ok {
		t.Errorf("EMPTY_OK key should be present (empty value), data=%v", sec.Data)
	}
}

// TestUnsetEnvVar_RemovesManagedSecretOnlyKey covers the unset fall-through:
// a key that exists ONLY in <svc>-secrets (never in spec.envVars) is removed
// from the Secret, leaving other keys intact.
func TestUnsetEnvVar_RemovesManagedSecretOnlyKey(t *testing.T) {
	t.Parallel()
	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      kube.ServiceSecretName("alpha", "web"),
			Namespace: "kuso",
			Annotations: map[string]string{
				"secrets.kuso.sislelabs.com/generated-KEEP": "hex32",
			},
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"WETRAVEL_API_KEY": []byte("sk-live-123"),
			"KEEP":             []byte("stay"),
		},
	}
	s := fakeServiceWithSecrets(t, []runtime.Object{existing},
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	if _, err := s.UnsetEnvVar(context.Background(), "alpha", "web", "WETRAVEL_API_KEY"); err != nil {
		t.Fatalf("UnsetEnvVar (managed-secret key): %v", err)
	}
	sec := getSecret(t, s, "kuso", kube.ServiceSecretName("alpha", "web"))
	if _, ok := sec.Data["WETRAVEL_API_KEY"]; ok {
		t.Errorf("WETRAVEL_API_KEY should be removed, data=%v", sec.Data)
	}
	if string(sec.Data["KEEP"]) != "stay" {
		t.Errorf("KEEP should survive, got %q", sec.Data["KEEP"])
	}
	if sec.Annotations["secrets.kuso.sislelabs.com/generated-KEEP"] != "hex32" {
		t.Errorf("annotation cleared on unset: %+v", sec.Annotations)
	}
}

// TestUnsetEnvVar_NotInSpecNorSecretIsNotFound: a name absent from both
// spec.envVars and the managed secret still returns ErrNotFound.
func TestUnsetEnvVar_NotInSpecNorSecretIsNotFound(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha", Port: 8080}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
	_, err := s.UnsetEnvVar(context.Background(), "alpha", "web", "GHOST")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}
