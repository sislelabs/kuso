package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

func TestEnvSleepFrom_ServeStale(t *testing.T) {
	t.Parallel()
	if got := envSleepFrom(&kube.KusoServiceSleep{Enabled: true, ServeStale: true}); got == nil || !got.Enabled || !got.ServeStale {
		t.Fatalf("enabled+serveStale = %+v", got)
	}
	// Non-production envs sleep via autoSleep, so serveStale must reach
	// them without sleep.enabled.
	if got := envSleepFrom(&kube.KusoServiceSleep{ServeStale: true}); got == nil || got.Enabled || !got.ServeStale {
		t.Fatalf("serveStale only = %+v", got)
	}
	if got := envSleepFrom(&kube.KusoServiceSleep{Enabled: true, ServeStale: true, WakeOn: &kube.KusoServiceWake{ExcludePaths: []string{"/hook"}}}); got != nil {
		t.Fatalf("excludePaths env is never activator-routed, got %+v", got)
	}
}

// AddService builds the production env from a hand-written literal that
// has dropped new fields before; serveStale must reach a fresh env.
func TestAddService_ServeStaleReachesProductionEnv(t *testing.T) {
	t.Parallel()
	s := defaultPodFixture(t, nil)
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "dockerfile",
		Sleep: &ServiceSleep{Enabled: true, ServeStale: true},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if env.Spec.Sleep == nil || !env.Spec.Sleep.Enabled || !env.Spec.Sleep.ServeStale {
		t.Fatalf("production env sleep = %+v, want enabled+serveStale", env.Spec.Sleep)
	}
}

func TestPatchService_ServeStalePropagatesToEnv(t *testing.T) {
	t.Parallel()
	s := defaultPodFixture(t, nil)
	ctx := context.Background()
	if _, err := s.AddService(ctx, "alpha", CreateServiceRequest{Name: "web", Runtime: "dockerfile", Sleep: &ServiceSleep{Enabled: true}}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	on := true
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Sleep: &PatchSleepRequest{ServeStale: &on}}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	env, err := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if env.Spec.Sleep == nil || !env.Spec.Sleep.Enabled || !env.Spec.Sleep.ServeStale {
		t.Fatalf("production env sleep after patch = %+v", env.Spec.Sleep)
	}
}
