package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// A cloned worker kept fromService: api, so every PRODUCTION api build was
// promoted onto worker-staging and the staging api's builds reached nobody.
func TestCreateEnvGroup_RetargetsWorkerFromServiceAtClonedSource(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("acme", kube.KusoProjectSpec{BaseDomain: "apps.example.com"}),
		seedService("acme", "api", kube.KusoServiceSpec{Project: "acme", Port: 8080}),
		seedService("acme", "worker", kube.KusoServiceSpec{Project: "acme", Runtime: "worker", FromService: "api"}),
		seedEnv("acme", "api", "production", "main", "acme-api-production"),
		seedEnv("acme", "worker", "production", "main", "acme-worker-production"),
	)
	ctx := context.Background()
	if _, err := s.CreateEnvGroup(ctx, "acme", CreateEnvGroupRequest{Name: "staging"}); err != nil {
		t.Fatalf("CreateEnvGroup: %v", err)
	}
	clone, err := s.Kube.GetKusoService(ctx, "kuso", "acme-worker-staging")
	if err != nil {
		t.Fatalf("get worker clone: %v", err)
	}
	if clone.Spec.FromService != "api-staging" {
		t.Errorf("worker-staging fromService = %q, want api-staging", clone.Spec.FromService)
	}
	prod, err := s.Kube.GetKusoService(ctx, "kuso", "acme-worker")
	if err != nil {
		t.Fatal(err)
	}
	if prod.Spec.FromService != "api" {
		t.Errorf("production worker fromService mutated to %q", prod.Spec.FromService)
	}
}
