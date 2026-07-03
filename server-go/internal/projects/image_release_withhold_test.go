package projects

import (
	"context"
	"reflect"
	"testing"

	"kuso/server/internal/kube"
)

func TestAddService_WithholdsImageWhenReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	img := &ServiceImageSpec{Repository: "ghcr.io/x/app", Tag: "v1"}
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "image", Port: 3000, Image: img,
		Release: &PatchReleaseRequest{Command: []string{"sh", "-c", "migrate"}},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if env.Spec.Image != nil {
		t.Errorf("image must be WITHHELD (nil) when a release hook is present, got %+v", env.Spec.Image)
	}
	want := &kube.KusoImage{Repository: "ghcr.io/x/app", Tag: "v1"}
	if !reflect.DeepEqual(env.Spec.PendingImage, want) {
		t.Errorf("pendingImage: got %+v, want %+v", env.Spec.PendingImage, want)
	}
}

func TestAddService_NoWithholdWithoutReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	img := &ServiceImageSpec{Repository: "ghcr.io/x/app", Tag: "v1"}
	_, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name: "web", Runtime: "image", Port: 3000, Image: img,
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	env, _ := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if env.Spec.Image == nil || env.Spec.Image.Tag != "v1" {
		t.Errorf("image must be live immediately when NO release hook: got %+v", env.Spec.Image)
	}
	if env.Spec.PendingImage != nil {
		t.Errorf("pendingImage must be nil when no release hook: got %+v", env.Spec.PendingImage)
	}
}
