package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// Workers are born hostless. Flipping one to a web runtime must stamp the
// default host back, or the env never gets an Ingress.
func TestPatchService_LeavingWorkerRestoresHost(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{BaseDomain: "example.com"}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
	)
	ctx := context.Background()
	if _, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "jobs", Runtime: "worker", FromService: "api"}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if _, err := s.AddEnvironment(ctx, "alpha", "jobs", CreateEnvRequest{Name: "staging", Branch: "dev", ShareAddons: true}); err != nil {
		t.Fatalf("AddEnvironment: %v", err)
	}
	rt := "dockerfile"
	if _, err := s.PatchService(ctx, "alpha", "jobs", PatchServiceRequest{Runtime: &rt}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	for name, want := range map[string]string{
		"alpha-jobs-production": "jobs.example.com",
		"alpha-jobs-staging":    "jobs-staging.example.com",
	} {
		env, err := s.Kube.GetKusoEnvironment(ctx, "kuso", name)
		if err != nil {
			t.Fatalf("get %s: %v", name, err)
		}
		if env.Spec.Host != want {
			t.Errorf("%s host = %q, want %q", name, env.Spec.Host, want)
		}
		if len(env.Spec.TLSHosts) != 1 || env.Spec.TLSHosts[0] != want {
			t.Errorf("%s tlsHosts = %v, want [%s]", name, env.Spec.TLSHosts, want)
		}
	}
	svc, _ := s.GetService(ctx, "alpha", "jobs")
	if svc.Spec.FromService != "" {
		t.Errorf("fromService = %q, want cleared", svc.Spec.FromService)
	}
}
