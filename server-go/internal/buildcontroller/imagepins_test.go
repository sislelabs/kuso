package buildcontroller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
)

// OPS-3: every image kuso itself picks for a build must have a pin slot,
// or release.sh never resolves it and that image runs by tag.
func TestBuildImageDigestsCoverDefaults(t *testing.T) {
	for _, ref := range []string{
		defaultBuildkitImage, defaultCacheInitImage,
		defaultEnvDetectImage + ":" + defaultEnvDetectTag,
		defaultNixpacksImage + ":" + defaultNixpacksVersion,
		defaultStaticBuilderImage, defaultStaticRuntimeImage, defaultBuildpacksImage,
		projects.NewStaticBuilderImage,
	} {
		if _, ok := buildImageDigests[ref]; !ok {
			t.Errorf("%s has no entry in buildImageDigests", ref)
		}
	}
}

// A resolved digest reaches the rendered pod; an image the user named
// runs as given.
func TestPinnedImageReachesBuildPod(t *testing.T) {
	const d = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	saved := buildImageDigests[defaultBuildkitImage]
	buildImageDigests[defaultBuildkitImage] = d
	t.Cleanup(func() { buildImageDigests[defaultBuildkitImage] = saved })

	b := &kube.KusoBuild{Spec: kube.KusoBuildSpec{Image: &kube.KusoImage{Repository: "registry.local/a/b", Tag: "sha"}}}
	b.Spec.Static = &kube.KusoStaticSpec{RuntimeImage: "example.com/my-nginx:1"}
	if got := renderStaticPlanContainer(b); envValue(got.Env, "RUNTIME_IMAGE") != "example.com/my-nginx:1" {
		t.Errorf("user runtime image rewritten: %q", envValue(got.Env, "RUNTIME_IMAGE"))
	}
	if got := renderBuildkitContainer(b, "dockerfile", corev1.ResourceRequirements{}).Image; got != defaultBuildkitImage+"@"+d {
		t.Errorf("buildkit container image = %q", got)
	}
}

func envValue(env []corev1.EnvVar, name string) string {
	for _, e := range env {
		if e.Name == name {
			return e.Value
		}
	}
	return ""
}
