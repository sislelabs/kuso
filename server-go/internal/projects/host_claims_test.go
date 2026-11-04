package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedHostedEnv(project, service, scope, name, host string, extra ...string) seed {
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", name, &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "kuso",
			Labels:    map[string]string{labelProject: project, labelService: service, labelEnv: scope},
		},
		Spec: kube.KusoEnvironmentSpec{
			Project:         project,
			Service:         serviceCRName(project, service),
			Kind:            scope,
			Host:            host,
			AdditionalHosts: extra,
		},
	})
}

// LIFE-02: the dashboard host (KUSO_DOMAIN) must never be claimable by an
// app env, explicitly or through a generated host.
func TestHostClaims_ControlPlaneHostReserved(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "kuso.example.com")
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedHostedEnv("alpha", "web", "production", "alpha-web-production", "web.alpha.kuso.example.com"),
		seedProject("shop", kube.KusoProjectSpec{BaseDomain: "kuso.example.com"}),
	)
	ctx := context.Background()
	if _, err := s.AddEnvDomain(ctx, "alpha", "web", "production", "KUSO.example.com", ""); !errors.Is(err, ErrConflict) {
		t.Errorf("AddEnvDomain(control-plane host) err = %v, want ErrConflict", err)
	}
	if _, err := s.SetEnvDomains(ctx, "alpha", "web", "production", []string{"kuso.example.com"}); !errors.Is(err, ErrConflict) {
		t.Errorf("SetEnvDomains(control-plane host) err = %v, want ErrConflict", err)
	}
	// service==project with baseDomain==KUSO_DOMAIN generates the apex.
	if _, err := s.AddService(ctx, "shop", CreateServiceRequest{Name: "shop"}); !errors.Is(err, ErrConflict) {
		t.Errorf("AddService(generated host = control-plane host) err = %v, want ErrConflict", err)
	}
}

// LIFE-07: another project's env already serving a host must block the
// claim; the conflict check used to see only the caller's project.
func TestHostClaims_CrossProjectConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedHostedEnv("alpha", "web", "production", "alpha-web-production", "web.alpha.example.com", "app.customer.com"),
		seedProject("beta", kube.KusoProjectSpec{}),
		seedService("beta", "web", kube.KusoServiceSpec{Project: "beta"}),
		seedHostedEnv("beta", "web", "production", "beta-web-production", "web.beta.example.com"),
	)
	ctx := context.Background()
	if _, err := s.AddEnvDomain(ctx, "beta", "web", "production", "app.customer.com", ""); !errors.Is(err, ErrConflict) {
		t.Errorf("AddEnvDomain(host of another project) err = %v, want ErrConflict", err)
	}
	if _, err := s.SetEnvDomains(ctx, "beta", "web", "production", []string{"app.customer.com"}); !errors.Is(err, ErrConflict) {
		t.Errorf("SetEnvDomains(host of another project) err = %v, want ErrConflict", err)
	}
	if _, err := s.AddDomain(ctx, "beta", "web", AddDomainRequest{Host: "app.customer.com"}); !errors.Is(err, ErrConflict) {
		t.Errorf("AddDomain(host of another project) err = %v, want ErrConflict", err)
	}
}

// Grandfathering: a host an env already holds (even one that now
// conflicts) must not block an unrelated domain edit on that env.
func TestHostClaims_ExistingHostsAreGrandfathered(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedHostedEnv("alpha", "web", "production", "alpha-web-production", "web.alpha.example.com", "dup.example.com"),
		seedProject("beta", kube.KusoProjectSpec{}),
		seedService("beta", "web", kube.KusoServiceSpec{Project: "beta"}),
		seedHostedEnv("beta", "web", "production", "beta-web-production", "web.beta.example.com", "dup.example.com"),
	)
	if _, err := s.SetEnvDomains(context.Background(), "beta", "web", "production", []string{"dup.example.com", "new.beta.com"}); err != nil {
		t.Fatalf("SetEnvDomains keeping an already-held host: %v", err)
	}
}

// LIFE-06: with service==project and a baseDomain, custom env "api" of
// service "shop" generates api.shop.com — the production host of a
// sibling service "api". Two Ingresses would claim the same host.
func TestHostClaims_GeneratedHostConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("shop", kube.KusoProjectSpec{BaseDomain: "shop.com"}),
		seedService("shop", "shop", kube.KusoServiceSpec{Project: "shop"}),
		seedService("shop", "api", kube.KusoServiceSpec{Project: "shop"}),
		seedHostedEnv("shop", "api", "production", "shop-api-production", "api.shop.com"),
	)
	_, err := s.AddEnvironment(context.Background(), "shop", "shop", CreateEnvRequest{Name: "api", Branch: "dev", ShareAddons: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AddEnvironment(generated host taken) err = %v, want ErrConflict", err)
	}
}

// LIFE-13: env "activator" collides with the activator ExternalName shape.
func TestAddEnvironment_ActivatorNameReserved(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
	)
	for _, name := range []string{"activator", "qa-activator"} {
		if _, err := s.AddEnvironment(context.Background(), "alpha", "api", CreateEnvRequest{Name: name, Branch: "dev", ShareAddons: true}); !errors.Is(err, ErrInvalid) {
			t.Errorf("AddEnvironment(%q) err = %v, want ErrInvalid", name, err)
		}
	}
}

// LIFE-05: a baseDomain change moved only production hosts; custom envs
// and env-group clones kept serving the old domain. User-set hosts stay.
func TestUpdate_BaseDomainMovesEveryGeneratedHost(t *testing.T) {
	t.Setenv("KUSO_DOMAIN", "kuso.example.com")
	const old = "old.example.com"
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x", DefaultBranch: "main"}, BaseDomain: old}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "web-qa", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "alpha", kube.KusoServiceSpec{Project: "alpha"}),
		seedHostedEnv("alpha", "web", "production", "alpha-web-production", "web."+old),
		seedHostedEnv("alpha", "web", "staging", "alpha-web-staging", "web-staging."+old),
		seedHostedEnv("alpha", "web", "demo", "alpha-web-demo", "demo.customer.org"),
		seedHostedEnv("alpha", "web-qa", "qa", "alpha-web-qa-production", "web-qa.alpha."+old),
		seedHostedEnv("alpha", "alpha", "production", "alpha-alpha-production", old),
	)
	ctx := context.Background()
	// Moving onto the control-plane host must not hand the project-named
	// service's apex to the dashboard's host.
	newBase := "kuso.example.com"
	if _, err := s.Update(ctx, "alpha", UpdateProjectRequest{BaseDomain: &newBase}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	for name, want := range map[string]string{
		"alpha-web-production":    "web.kuso.example.com",
		"alpha-web-staging":       "web-staging.kuso.example.com",
		"alpha-web-demo":          "demo.customer.org",
		"alpha-web-qa-production": "web-qa.alpha.kuso.example.com",
		"alpha-alpha-production":  old,
	} {
		env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if env.Spec.Host != want {
			t.Errorf("%s host = %q, want %q", name, env.Spec.Host, want)
		}
	}
}
