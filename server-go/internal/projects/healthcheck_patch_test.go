package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func healthcheckFixture(t *testing.T) *Service {
	t.Helper()
	return fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
		}),
		typedSeed(kube.GVRServices, "KusoService", serviceCRName("alpha", "web"), &kube.KusoService{
			ObjectMeta: metav1.ObjectMeta{
				Name:      serviceCRName("alpha", "web"),
				Namespace: "kuso",
				Labels:    map[string]string{labelProject: "alpha", labelService: "web"},
			},
			Spec: kube.KusoServiceSpec{Project: "alpha", Port: 3000, Runtime: "nixpacks"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)
}

// A healthcheck-only PATCH must reach the env CR: the chart renders probes
// off the env, and without a changed flag propagation returns early.
func TestPatchService_HealthcheckOnly_ReachesEnvCRs(t *testing.T) {
	t.Parallel()
	s := healthcheckFixture(t)
	ctx := context.Background()

	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Healthcheck: &kube.KusoHealthcheck{Path: " /healthz ", PeriodSeconds: 15, TimeoutSeconds: 3, FailureThreshold: 4},
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	env, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	hc := env.Spec.Healthcheck
	if hc == nil || hc.Path != "/healthz" || hc.PeriodSeconds != 15 || hc.TimeoutSeconds != 3 || hc.FailureThreshold != 4 {
		t.Fatalf("env healthcheck = %+v, want path /healthz period 15 timeout 3 threshold 4", hc)
	}

	// An empty path removes the check from service and env alike.
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{
		Healthcheck: &kube.KusoHealthcheck{},
	}); err != nil {
		t.Fatalf("PatchService clear: %v", err)
	}
	env, err = s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("get env: %v", err)
	}
	if env.Spec.Healthcheck != nil {
		t.Errorf("env healthcheck after clear = %+v, want nil (TCP probes)", env.Spec.Healthcheck)
	}
	svc, err := s.GetService(ctx, "alpha", "web")
	if err != nil {
		t.Fatalf("get service: %v", err)
	}
	if svc.Spec.Healthcheck != nil {
		t.Errorf("service healthcheck after clear = %+v, want nil", svc.Spec.Healthcheck)
	}
}

func TestPatchService_HealthcheckValidation(t *testing.T) {
	t.Parallel()
	cases := map[string]kube.KusoHealthcheck{
		"relative path":         {Path: "healthz"},
		"protocol-relative":     {Path: "//evil"},
		"whitespace in path":    {Path: "/a b"},
		"port out of range":     {Path: "/h", Port: 70000},
		"negative period":       {Path: "/h", PeriodSeconds: -1},
		"huge threshold":        {Path: "/h", FailureThreshold: 500},
		"timeout beyond period": {Path: "/h", PeriodSeconds: 5, TimeoutSeconds: 10},
	}
	for name, hc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := healthcheckFixture(t)
			_, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{Healthcheck: &hc})
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
