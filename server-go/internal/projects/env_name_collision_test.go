package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// Env "worker" on service "api" is CR alpha-api-worker, the same name (and
// the same <name>-secrets) as service "api-worker". Deleting such an env
// wiped the sibling's managed Secret.
func TestAddEnvironment_RefusesNameOfSiblingService(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedService("alpha", "api-worker", kube.KusoServiceSpec{Project: "alpha"}),
	)
	_, err := s.AddEnvironment(context.Background(), "alpha", "api", CreateEnvRequest{Name: "worker", Branch: "dev", ShareAddons: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AddEnvironment err = %v, want ErrConflict", err)
	}
}

func TestAddService_RefusesNameOfSiblingEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api", "worker", "dev", "alpha-api-worker"),
	)
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{Name: "api-worker"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AddService err = %v, want ErrConflict", err)
	}
}

func TestAddEnvironment_DuplicateIsConflict(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "api", kube.KusoServiceSpec{Project: "alpha"}),
		seedEnv("alpha", "api", "staging", "dev", "alpha-api-staging"),
	)
	_, err := s.AddEnvironment(context.Background(), "alpha", "api", CreateEnvRequest{Name: "staging", Branch: "dev", ShareAddons: true})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("AddEnvironment err = %v, want ErrConflict", err)
	}
}
