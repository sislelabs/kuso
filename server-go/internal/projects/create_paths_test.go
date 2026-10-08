package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func seedEnvWithHost(project, service, kind, name, host string) seed {
	sd := seedEnv(project, service, kind, "main", name)
	if err := setEnvHostInSeed(sd, host); err != nil {
		panic(err)
	}
	return sd
}

func TestAddEnvironment_HostOverrideValidatedAndConflictChecked(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithHost("alpha", "web", "production", "alpha-web-production", "tickero.bg"),
	)
	ctx := context.Background()
	_, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "dev", HostOverride: "Tickero.bg", ShareAddons: true})
	if !errors.Is(err, ErrConflict) {
		t.Errorf("production host as staging --host: err = %v, want ErrConflict", err)
	}
	_, err = s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "dev", HostOverride: "Foo_Bar", ShareAddons: true})
	if !errors.Is(err, ErrInvalid) {
		t.Errorf("invalid --host: err = %v, want ErrInvalid", err)
	}
	env, err := s.AddEnvironment(ctx, "alpha", "web", CreateEnvRequest{Name: "staging", Branch: "dev", HostOverride: " Staging.Tickero.bg ", ShareAddons: true})
	if err != nil {
		t.Fatalf("valid --host: %v", err)
	}
	if env.Spec.Host != "staging.tickero.bg" {
		t.Errorf("host = %q, want normalised staging.tickero.bg", env.Spec.Host)
	}
}

func TestAddService_DomainConflictChecked(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnvWithHost("alpha", "web", "production", "alpha-web-production", "tickero.bg"),
	)
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "api", Domains: []ServiceDomain{{Host: "tickero.bg", TLS: true}}})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AddService err = %v, want ErrConflict", err)
	}
}

// kuso.yaml's internal/egress/volumes must be on the production env from
// the first reconcile, not after a second apply.
func TestAddService_BornWithNetworkAndVolumes(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}))
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "api", Internal: true, PrivateEgress: true, WaitForCI: true,
		Volumes: []VolumePatch{{Name: "data", MountPath: "/data", SizeGi: 2}},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if !env.Spec.Internal || !env.Spec.PrivateEgress {
		t.Errorf("env internal/privateEgress = %v/%v, want true/true", env.Spec.Internal, env.Spec.PrivateEgress)
	}
	if len(env.Spec.Volumes) != 1 || env.Spec.Volumes[0].MountPath != "/data" {
		t.Errorf("env volumes = %+v", env.Spec.Volumes)
	}
	svc, _ := s.GetService(context.Background(), "alpha", "api")
	if !svc.Spec.WaitForCI {
		t.Error("service waitForCI dropped")
	}
}

// The GitLab token used to be stored before validation, so a 400 left an
// orphan Secret holding the credential.
func TestAddService_InvalidRequestLeavesNoRepoToken(t *testing.T) {
	t.Parallel()
	s := fakeServiceWithSecrets(t, nil, seedProject("alpha", kube.KusoProjectSpec{}))
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:  "api",
		Repo:  &CreateServiceRepo{URL: "https://gitlab.com/g/app.git", Token: "glpat-x"},
		Sleep: &ServiceSleep{NonProduction: "bogus"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("AddService err = %v, want ErrInvalid", err)
	}
	if _, gerr := s.Kube.Clientset.CoreV1().Secrets("kuso").Get(context.Background(), "alpha-api-repo-token", metav1.GetOptions{}); gerr == nil {
		t.Fatal("repo token Secret left behind by a refused create")
	}
}
