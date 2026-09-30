package builds

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

func TestPromotionIndex_LiveEnvsFromPromotedBuildAnnotation(t *testing.T) {
	t.Parallel()
	prod := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-production", Namespace: "kuso",
			Labels:      map[string]string{kube.LabelProject: "alpha", kube.LabelService: "web", kube.LabelEnv: "production"},
			Annotations: map[string]string{annPromotedBuild: "alpha-web-b1"},
		},
		// Same image tag as b2: tag matching would wrongly call b2 live.
		Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Image: &kube.KusoImage{Tag: "shared"}},
	}
	preview := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name: "alpha-web-pr-7", Namespace: "kuso",
			Labels:      map[string]string{kube.LabelProject: "alpha", kube.LabelService: "web", kube.LabelEnv: "preview-pr-7"},
			Annotations: map[string]string{annPromotedBuild: "alpha-web-b1"},
		},
		Spec: kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web", Branch: "feature-x"},
	}
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", prod),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", preview),
	)
	idx, err := s.PromotionIndex(context.Background(), "alpha", "web")
	if err != nil {
		t.Fatalf("PromotionIndex: %v", err)
	}
	got := idx.LiveEnvs("alpha-web-b1")
	if len(got) != 2 || got[0] != "preview-pr-7" || got[1] != "production" {
		t.Errorf("LiveEnvs(b1) = %v, want [preview-pr-7 production]", got)
	}
	if got := idx.LiveEnvs("alpha-web-b2"); len(got) != 0 {
		t.Errorf("LiveEnvs(b2) = %v, want none", got)
	}
	if r := idx.NotPromotedReason("main"); r != "" {
		t.Errorf("main is deployed by production; reason = %q", r)
	}
	if r := idx.NotPromotedReason("orphan"); r == "" {
		t.Error("no env deploys branch orphan; want a reason")
	}
}

// The build-log prune keeps these, so every env's live build must be
// listed across every namespace it scans, and envs never promoted add
// nothing.
func TestLiveBuildNames(t *testing.T) {
	t.Parallel()
	env := func(name, ns, build string) *kube.KusoEnvironment {
		e := &kube.KusoEnvironment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
		if build != "" {
			e.Annotations = map[string]string{annPromotedBuild: build}
		}
		return e
	}
	s := fakeService(t,
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env("alpha-web-production", "kuso", "alpha-web-b1")),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env("alpha-web-pr-7", "kuso", "alpha-web-b1")),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env("beta-api-production", "kuso", "beta-api-b9")),
		typedSeed(kube.GVREnvironments, "KusoEnvironment", env("gamma-new-production", "kuso", "")),
	)
	got, err := s.LiveBuildNames(context.Background(), []string{"kuso", "kuso-beta"})
	if err != nil {
		t.Fatalf("LiveBuildNames: %v", err)
	}
	if len(got) != 2 || got[0] != "alpha-web-b1" || got[1] != "beta-api-b9" {
		t.Errorf("LiveBuildNames = %v, want [alpha-web-b1 beta-api-b9]", got)
	}
}
