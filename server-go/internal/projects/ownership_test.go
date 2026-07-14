package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// ---- finding 5: qualified-name cross-project access ----------------------
//
// serviceCRName accepts already-qualified input ("<project>-<service>").
// With overlapping project names — "foo" and "foo-bar" — a member of foo
// passing service="foo-bar-web" resolves to the CR "foo-bar-web", which is
// foo-bar's service "web". Every resolution helper must therefore verify
// the FETCHED CR's spec.project before returning or mutating it.

// overlapFixture seeds projects foo + foo-bar, foo's service "api" and
// foo-bar's service "web" (CR name "foo-bar-web" — exactly the shape a
// foo-authorized caller can address as a "qualified" name).
func overlapFixture(t *testing.T) *Service {
	t.Helper()
	return fakeService(t,
		seedProject("foo", kube.KusoProjectSpec{}),
		seedProject("foo-bar", kube.KusoProjectSpec{}),
		seedService("foo", "api", kube.KusoServiceSpec{Project: "foo"}),
		seedService("foo-bar", "web", kube.KusoServiceSpec{Project: "foo-bar"}),
	)
}

func TestGetService_RejectsCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapFixture(t)
	ctx := context.Background()

	// foo member addressing foo-bar's CR by its full name must 404.
	if _, err := s.GetService(ctx, "foo", "foo-bar-web"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetService(foo, foo-bar-web): want ErrNotFound, got %v", err)
	}
	// The legitimate owner still reads it, in both short and FQN form.
	if _, err := s.GetService(ctx, "foo-bar", "web"); err != nil {
		t.Fatalf("GetService(foo-bar, web): %v", err)
	}
	if _, err := s.GetService(ctx, "foo-bar", "foo-bar-web"); err != nil {
		t.Fatalf("GetService(foo-bar, foo-bar-web): %v", err)
	}
	// foo's own service still resolves.
	if _, err := s.GetService(ctx, "foo", "api"); err != nil {
		t.Fatalf("GetService(foo, api): %v", err)
	}
}

func TestServiceWrites_RejectCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := overlapFixture(t)
	ctx := context.Background()

	if _, err := s.SetEnvVar(ctx, "foo", "foo-bar-web", "PWNED", SetEnvVarRequest{Value: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnvVar: want ErrNotFound, got %v", err)
	}
	if err := s.SetEnv(ctx, "foo", "foo-bar-web", []EnvVar{{Name: "PWNED", Value: "x"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnv: want ErrNotFound, got %v", err)
	}
	if _, err := s.AddDomain(ctx, "foo", "foo-bar-web", AddDomainRequest{Host: "evil.example.com"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddDomain: want ErrNotFound, got %v", err)
	}
	if _, err := s.PatchService(ctx, "foo", "foo-bar-web", PatchServiceRequest{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("PatchService: want ErrNotFound, got %v", err)
	}
	if err := s.DeleteService(ctx, "foo", "foo-bar-web"); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteService: want ErrNotFound, got %v", err)
	}
	if _, err := s.SetSharedEnvKeys(ctx, "foo", "foo-bar-web", []string{"KEY"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSharedEnvKeys: want ErrNotFound, got %v", err)
	}
	if _, err := s.SetSubscribedAddons(ctx, "foo", "foo-bar-web", []string{"pg"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetSubscribedAddons: want ErrNotFound, got %v", err)
	}

	// The victim CR must be untouched by all of the above.
	svc, err := s.GetService(ctx, "foo-bar", "web")
	if err != nil {
		t.Fatalf("victim service gone: %v", err)
	}
	if len(svc.Spec.EnvVars) != 0 || len(svc.Spec.Domains) != 0 {
		t.Errorf("victim CR mutated: %+v", svc.Spec)
	}
}

func TestEnvScopedWrites_RejectCrossProjectQualifiedName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("foo", kube.KusoProjectSpec{}),
		seedProject("foo-bar", kube.KusoProjectSpec{}),
		seedService("foo-bar", "web", kube.KusoServiceSpec{Project: "foo-bar"}),
		seedEnv("foo-bar", "web", "production", "main", "foo-bar-web-production"),
	)
	ctx := context.Background()

	// envCRNameFor("foo", "foo-bar-web", "production") lands on
	// "foo-bar-web-production" — foo-bar's production env.
	if _, err := s.AddEnvDomain(ctx, "foo", "foo-bar-web", "production", "evil.example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("AddEnvDomain: want ErrNotFound, got %v", err)
	}
	if _, err := s.SetEnvDomains(ctx, "foo", "foo-bar-web", "production", []string{"evil.example.com"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnvDomains: want ErrNotFound, got %v", err)
	}
	if _, err := s.SetEnvScopedVar(ctx, "foo", "foo-bar-web", "production", "PWNED", SetEnvVarRequest{Value: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetEnvScopedVar: want ErrNotFound, got %v", err)
	}

	env, err := s.GetEnvironment(ctx, "foo-bar", "foo-bar-web-production")
	if err != nil {
		t.Fatalf("victim env gone: %v", err)
	}
	if len(env.Spec.AdditionalHosts) != 0 || len(env.Spec.EnvVars) != 0 {
		t.Errorf("victim env mutated: %+v", env.Spec)
	}
}

// ---- finding 3: create-time env vars bypassed secret-ref validation ------

func TestAddService_RejectsForeignSecretRef(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))

	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		EnvVars: []EnvVar{{
			Name: "STOLEN_DSN",
			ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "beta-pg-conn", "key": "DATABASE_URL"},
			},
		}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for foreign secretKeyRef at create, got %v", err)
	}
	// Nothing may have been persisted.
	if _, gerr := s.GetService(context.Background(), "alpha", "web"); !errors.Is(gerr, ErrNotFound) {
		t.Fatalf("service must not exist after rejected create, got %v", gerr)
	}
}

// A non-secretKeyRef valueFrom (here a configMapKeyRef) must be carried
// through UNCHANGED rather than rejected. It is not a cross-project
// SECRET vector (it reads a ConfigMap, not another project's -conn
// Secret), and the KusoService CRD's valueFrom schema is closed to
// secretKeyRef only — so any configMapKeyRef is PRUNED by the apiserver
// on write anyway. Rejecting it here was the C3 regression: because
// SetEnv/AddService/spec.Apply do a full-list replace, a SINGLE legacy
// entry on a pre-upgrade service made every subsequent env save 400.
func TestAddService_PreservesNonSecretKeyRefValueFrom(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))

	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		EnvVars: []EnvVar{
			{Name: "PLAIN", Value: "1"},
			{Name: "FROM_CONFIGMAP", ValueFrom: map[string]any{
				"configMapKeyRef": map[string]any{"name": "cluster-config", "key": "anything"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("configMapKeyRef must not fail the save (C3): %v", err)
	}
	var found bool
	for _, ev := range created.Spec.EnvVars {
		if ev.Name == "FROM_CONFIGMAP" {
			found = true
			if _, ok := ev.ValueFrom["configMapKeyRef"]; !ok {
				t.Errorf("configMapKeyRef was not carried through unchanged: %+v", ev.ValueFrom)
			}
		}
	}
	if !found {
		t.Error("FROM_CONFIGMAP env var was dropped")
	}
}

// After a legacy non-secretKeyRef var is present, an UNRELATED valid var
// can still be added/saved — the full-list replace must not 400 on the
// pre-existing configMapKeyRef entry (C3 regression: it did).
func TestSetEnv_LegacyConfigMapRef_DoesNotBlockLaterSave(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "dockerfile",
		EnvVars: []EnvVar{
			{Name: "LEGACY", ValueFrom: map[string]any{
				"configMapKeyRef": map[string]any{"name": "cfg", "key": "K"},
			}},
			{Name: "NEW", Value: "1"},
		},
	}); err != nil {
		t.Fatalf("save with legacy configMapKeyRef + new var failed (C3): %v", err)
	}
}

func TestAddService_AllowsOwnedSecretRefs(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))

	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		EnvVars: []EnvVar{
			{Name: "PLAIN", Value: "1"},
			{Name: "SHARED_TOKEN", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-shared", "key": "TOKEN"},
			}},
			{Name: "OWN_SECRET", ValueFrom: map[string]any{
				"secretKeyRef": map[string]any{"name": "alpha-web-secrets", "key": "S"},
			}},
		},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if len(created.Spec.EnvVars) != 3 {
		t.Fatalf("envVars: %+v", created.Spec.EnvVars)
	}
	// The env list must have flowed onto the auto-created production
	// env too (the second copy the finding called out).
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if len(env.Spec.EnvVars) != 3 {
		t.Errorf("production env vars: %+v", env.Spec.EnvVars)
	}
}

func TestAddService_ValidatesEnvVarNames(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
	}))

	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		EnvVars: []EnvVar{{Name: "1BAD NAME", Value: "x"}},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for malformed env name at create, got %v", err)
	}
}
