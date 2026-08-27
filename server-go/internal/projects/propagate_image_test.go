package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// TestPatchService_PropagatesImageToEnvs is the regression for the
// runtime=image redeploy no-op found 2026-08-27.
//
// runtime=image services skip the build pipeline entirely — builds.Create
// refuses them and points the user at PatchService. But changedFields had
// no Image entry, so a redeploy wrote the new tag to the SERVICE spec and
// it never reached any env CR. The pods kept running the old image while
// the API reported the new one, and the drift detector (which does not
// compare image either) showed everything in sync.
func TestPatchService_PropagatesImageToEnvs(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "image",
			Image:   &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "v1"},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
		seedEnv("alpha", "web", "staging", "stage", "alpha-web-staging"),
	)

	img := ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v2"}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Image: &img,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	for name, env := range envByName(t, s, "alpha", "web") {
		if env.Spec.Image == nil {
			t.Errorf("%s: image never reached the env CR — redeploy is a silent no-op", name)
			continue
		}
		if env.Spec.Image.Tag != "v2" {
			t.Errorf("%s: env still on tag %q, want v2", name, env.Spec.Image.Tag)
		}
	}
}

// A runtime=image service WITH a release hook must withhold the image
// behind pendingImage, so an un-migrated image never serves. The
// imagerelease watcher runs the hook and promotes pendingImage→image.
// This mirrors the split AddService/AddEnvironment already apply at env
// creation; propagation must not bypass it.
func TestPatchService_WithholdsImageBehindReleaseHook(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{
			Runtime: "image",
			Image:   &kube.KusoImage{Repository: "ghcr.io/acme/web", Tag: "v1"},
			Release: &kube.KusoReleaseSpec{Command: []string{"sh", "-c", "migrate up"}},
		}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	img := ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v2"}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Image: &img,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	env := envByName(t, s, "alpha", "web")["alpha-web-production"]
	if env.Spec.PendingImage == nil || env.Spec.PendingImage.Tag != "v2" {
		t.Errorf("release-hooked service should stage the image in pendingImage; got %+v",
			env.Spec.PendingImage)
	}
	if env.Spec.Image != nil && env.Spec.Image.Tag == "v2" {
		t.Error("un-migrated image was promoted straight to spec.image, bypassing the release hook")
	}
}

// A BUILT service (dockerfile/nixpacks) must be untouched by this path —
// its image is promoted by the build poller after a successful build, and
// writing the service's image field onto envs here would fight it.
func TestPatchService_DoesNotPropagateImageForBuiltRuntimes(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", kube.KusoProjectSpec{}),
		seedService("alpha", "web", kube.KusoServiceSpec{Runtime: "dockerfile"}),
		seedEnv("alpha", "web", "production", "main", "alpha-web-production"),
	)

	img := ServiceImageSpec{Repository: "ghcr.io/acme/web", Tag: "v9"}
	if _, err := s.PatchService(context.Background(), "alpha", "web", PatchServiceRequest{
		Image: &img,
	}); err != nil {
		t.Fatalf("PatchService: %v", err)
	}

	env := envByName(t, s, "alpha", "web")["alpha-web-production"]
	if env.Spec.Image != nil && env.Spec.Image.Tag == "v9" {
		t.Error("image propagated onto a BUILT service's env; the build poller owns that field")
	}
}
