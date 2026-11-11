package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// SEC-2: a runtime=image service may not run another project's image from
// the in-cluster registry (kubelets pull it with the all-repo node
// credential).
func TestAddService_RejectsForeignInClusterImage(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}))
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "steal", Runtime: "image", Port: 8080,
		Image: &ServiceImageSpec{Repository: "kuso-registry.kuso.svc.cluster.local:5000/beta/api", Tag: "abc"},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("foreign in-cluster image: err = %v, want ErrInvalid", err)
	}
	if _, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "own", Runtime: "image", Port: 8080,
		Image: &ServiceImageSpec{Repository: "kuso-registry.kuso.svc.cluster.local:5000/alpha/api", Tag: "abc"},
	}); err != nil {
		t.Fatalf("own in-cluster image: %v", err)
	}
}
