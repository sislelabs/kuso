package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

func hostUsersOff(sc *kube.KusoSecurityContext) bool {
	return sc != nil && sc.HostUsers != nil && !*sc.HostUsers
}

// The production env is minted by AddService's own env literal, the path
// that has silently dropped new service fields before.
func TestAddService_CopiesHostUsersToProductionEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	f := false
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:            "web",
		Runtime:         "image",
		Port:            3000,
		Image:           &ServiceImageSpec{Repository: "ghcr.io/x/y", Tag: "v1"},
		SecurityContext: &kube.KusoSecurityContext{HostUsers: &f},
	}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("env not auto-created: %v", err)
	}
	if !hostUsersOff(env.Spec.SecurityContext) {
		t.Errorf("production env securityContext = %+v, want hostUsers false", env.Spec.SecurityContext)
	}
}

func TestPatchService_HostUsersPropagatesAndResets(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Port: 8080, Runtime: "dockerfile"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)
	ctx := context.Background()
	f, tr := false, true

	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		SecurityContext: &kube.KusoSecurityContext{HostUsers: &f},
	}); err != nil {
		t.Fatalf("PatchService on: %v", err)
	}
	for name, env := range envByName(t, s, "alpha", "web") {
		if !hostUsersOff(env.Spec.SecurityContext) {
			t.Errorf("%s: securityContext = %+v, want hostUsers false", name, env.Spec.SecurityContext)
		}
	}

	// true is the no-user-namespace default, so it stores as nil like any
	// other context that changes nothing.
	svc, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		SecurityContext: &kube.KusoSecurityContext{HostUsers: &tr},
	})
	if err != nil {
		t.Fatalf("PatchService off: %v", err)
	}
	if svc.Spec.SecurityContext != nil {
		t.Errorf("service securityContext = %+v, want nil", svc.Spec.SecurityContext)
	}
	for name, env := range envByName(t, s, "alpha", "web") {
		if env.Spec.SecurityContext != nil {
			t.Errorf("%s: securityContext = %+v, want nil after reset", name, env.Spec.SecurityContext)
		}
	}
}
