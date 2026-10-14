package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

// A slug "shop-api" in project "shop" made the service CR "shop-api" while
// the production env became "shop-shop-api-production", so every env-scoped
// write (domains, env set --env, wake) targeted a CR that doesn't exist.
func TestAddService_RefusesProjectPrefixedName(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("shop", kube.KusoProjectSpec{}))
	_, err := s.AddService(context.Background(), "shop", CreateServiceRequest{Name: "shop-api"})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("AddService err = %v, want ErrInvalid", err)
	}
	if _, gerr := s.Kube.GetKusoService(context.Background(), "kuso", "shop-api"); gerr == nil {
		t.Fatal("service CR shop-api was created despite the refusal")
	}
}

func TestRenameService_RefusesProjectPrefixedName(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("shop", kube.KusoProjectSpec{}),
		seedService("shop", "api", kube.KusoServiceSpec{Project: "shop"}),
	)
	_, err := s.RenameService(context.Background(), "shop", "api", "shop-web")
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("RenameService err = %v, want ErrInvalid", err)
	}
}
