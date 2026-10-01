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

// The imagerelease watcher gives up on a tag after 3 failed hooks and
// marks the env. Re-setting the same image must clear that marker so the
// release is retried; otherwise a retry needed a new tag.
func TestPatchService_SameImageClearsReleaseGiveUp(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))
	ctx := context.Background()
	img := &ServiceImageSpec{Repository: "ghcr.io/x/app", Tag: "v1"}
	if _, err := s.AddService(ctx, "alpha", CreateServiceRequest{
		Name: "web", Runtime: "image", Port: 3000, Image: img,
		Release: &PatchReleaseRequest{Command: []string{"migrate"}},
	}); err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if _, err := s.Kube.UpdateKusoEnvironmentWithRetry(ctx, "kuso", "alpha-web-production", func(e *kube.KusoEnvironment) error {
		if e.Annotations == nil {
			e.Annotations = map[string]string{}
		}
		e.Annotations["kuso.sislelabs.com/release-failed-image"] = "ghcr.io/x/app:v1"
		e.Annotations["kuso.sislelabs.com/release-failed-attempts"] = "3"
		e.Annotations["kuso.sislelabs.com/release-failed-at"] = "2026-10-01T00:00:00Z"
		return nil
	}); err != nil {
		t.Fatalf("seed annotations: %v", err)
	}
	if _, err := s.PatchService(ctx, "alpha", "web", PatchServiceRequest{Image: img}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}
	env, _ := s.GetEnvironment(ctx, "alpha", "alpha-web-production")
	for _, k := range []string{"release-failed-image", "release-failed-attempts", "release-failed-at"} {
		if _, ok := env.Annotations["kuso.sislelabs.com/"+k]; ok {
			t.Errorf("annotation %s not cleared: %v", k, env.Annotations)
		}
	}
}
