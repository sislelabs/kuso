package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// A cron is owned by its service CR, so the old service's teardown would GC
// it. Rename must refuse instead of silently dropping it.
func TestRenameService_RefusesWithCrons(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api", "production", "main", "alpha-api-production"),
		typedSeed(kube.GVRCrons, "KusoCron", "alpha-api-nightly", &kube.KusoCron{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-nightly", Namespace: "kuso"},
			Spec:       kube.KusoCronSpec{Project: "alpha", Service: "alpha-api", Schedule: "0 3 * * *"},
		}),
	)
	_, err := s.RenameService(context.Background(), "alpha", "api", "backend")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("RenameService err = %v, want ErrInvalid", err)
	}
	if _, gerr := s.GetService(context.Background(), "alpha", "api"); gerr != nil {
		t.Errorf("old service gone after refused rename: %v", gerr)
	}
}

func TestRenameService_RepointsFromServiceWorkers(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api", "production", "main", "alpha-api-production"),
		seedService("alpha", "worker", kube.KusoServiceSpec{Project: "alpha", Runtime: "worker", FromService: "api"}),
	)
	if _, err := s.RenameService(context.Background(), "alpha", "api", "backend"); err != nil {
		t.Fatalf("RenameService: %v", err)
	}
	w, err := s.GetService(context.Background(), "alpha", "worker")
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if w.Spec.FromService != "backend" {
		t.Errorf("worker fromService = %q, want backend", w.Spec.FromService)
	}
}
