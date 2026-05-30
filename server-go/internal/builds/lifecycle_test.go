package builds

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Rollback re-points an env CR at a prior SUCCEEDED build's image —
// the user-facing "roll back to a known-good deploy" path. These tests
// pin its branches: a succeeded build patches the env image; anything
// else refuses (rather than silently deploying a half-built or missing
// image). Driven through the public Service.Rollback against a fake
// apiserver.

// seedSucceededBuild creates a KusoBuild stamped phase=succeeded with a
// resolved image, mirroring what the poller writes on a clean build.
func seedSucceededBuild(project, service, name, repo, tag string) seed {
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   "kuso",
			Annotations: map[string]string{annPhase: "succeeded"},
		},
		Spec: kube.KusoBuildSpec{
			Project: project,
			Service: project + "-" + service,
			Image:   &kube.KusoImage{Repository: repo, Tag: tag},
		},
	}
	return seedBuild(b)
}

func TestRollback_SucceededBuildPatchesEnvImage(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
	)

	env, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-oldsha")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if env.Spec.Image == nil {
		t.Fatal("rolled-back env has no image")
	}
	if env.Spec.Image.Repository != "reg/alpha/web" || env.Spec.Image.Tag != "oldsha123456" {
		t.Errorf("env image = %s:%s, want reg/alpha/web:oldsha123456",
			env.Spec.Image.Repository, env.Spec.Image.Tag)
	}
	// The promoted-build annotation records which build we rolled to,
	// so a concurrent auto-promote can be reasoned about.
	if env.Annotations[annPromotedBuild] != "alpha-web-oldsha" {
		t.Errorf("promoted-build annotation = %q, want alpha-web-oldsha", env.Annotations[annPromotedBuild])
	}
}

func TestRollback_DefaultsToProductionEnv(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedSucceededBuild("alpha", "web", "alpha-web-oldsha", "reg/alpha/web", "oldsha123456"),
	)
	// Empty envName must resolve to "production".
	env, err := s.Rollback(context.Background(), "alpha", "web", "", "alpha-web-oldsha")
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if env.Name != "alpha-web-production" {
		t.Errorf("rolled-back env = %q, want alpha-web-production", env.Name)
	}
}

func TestRollback_RefusesNonSucceededBuild(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "alpha-web-failed",
			Namespace:   "kuso",
			Annotations: map[string]string{annPhase: "failed"},
		},
		Spec: kube.KusoBuildSpec{
			Project: "alpha",
			Service: "alpha-web",
			Image:   &kube.KusoImage{Repository: "reg/alpha/web", Tag: "failsha"},
		},
	}
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedBuild(b),
	)
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-failed"); err == nil {
		t.Fatal("expected Rollback to refuse a non-succeeded build, got nil error")
	}
}

func TestRollback_RefusesBuildWithNoImage(t *testing.T) {
	t.Parallel()
	b := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "alpha-web-noimg",
			Namespace:   "kuso",
			Annotations: map[string]string{annPhase: "succeeded"},
		},
		Spec: kube.KusoBuildSpec{Project: "alpha", Service: "alpha-web"}, // Image nil
	}
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
		seedBuild(b),
	)
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-noimg"); err == nil {
		t.Fatal("expected Rollback to refuse a succeeded build with no image, got nil error")
	}
}

func TestRollback_MissingBuildErrors(t *testing.T) {
	t.Parallel()
	// No RecordLookup wired → a NotFound build CR with no archive
	// fallback must surface an error, not patch the env.
	s := fakeService(t,
		seedService("alpha", "web"),
		seedProductionEnv("alpha", "web"),
	)
	if _, err := s.Rollback(context.Background(), "alpha", "web", "production", "alpha-web-ghost"); err == nil {
		t.Fatal("expected Rollback to error on a missing build, got nil")
	}
}
