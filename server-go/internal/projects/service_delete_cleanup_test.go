package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// TestDeleteService_CleansServiceSecret is the HIGH-6b guard: deleting a
// service must invoke the service-level secret cleanup, so a recreated
// service at the same name doesn't inherit the dead one's managed secret.
func TestDeleteService_CleansServiceSecret(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Project: "alpha"}),
	)

	var cleanedService string
	s.SecretsCleanupForService = func(_ context.Context, project, service string) error {
		cleanedService = project + "/" + service
		return nil
	}
	// Per-env cleanup is also wired so DeleteEnvironment doesn't skip it.
	s.SecretsCleanupForEnv = func(_ context.Context, _, _, _ string) error { return nil }

	if err := s.DeleteService(context.Background(), "alpha", "web"); err != nil {
		t.Fatalf("DeleteService: %v", err)
	}
	if cleanedService != "alpha/web" {
		t.Fatalf("service secret cleanup not invoked (got %q, want alpha/web)", cleanedService)
	}
}
