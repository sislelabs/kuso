package addons

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Deleting an env-scoped clone rewrites only the envs in that scope; a
// project-wide refresh rewrote (and could roll) every env for a conn only
// one scope ever mounted.
func TestDelete_EnvScopedCloneRefreshesOnlyItsScope(t *testing.T) {
	t.Parallel()
	prodFrom := []string{"alpha-web-secrets"}
	s := fakeService(t,
		seedProj("alpha"),
		typedSeed(kube.GVRAddons, "KusoAddon", &kube.KusoAddon{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-cache-pr-7", Namespace: "kuso", Labels: map[string]string{
				"kuso.sislelabs.com/project": "alpha", kube.LabelEnv: "preview-pr-7",
			}},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "redis"},
		}),
		seedEnvWithSecrets("alpha", "web", "production", "alpha-web-production", nil, prodFrom),
		seedEnvWithSecrets("alpha", "web", "preview-pr-7", "alpha-web-pr-7", nil,
			[]string{"alpha-cache-pr-7-conn", "alpha-web-pr-7-secrets"}),
	)
	if err := s.Delete(context.Background(), "alpha", "cache-pr-7"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	prod, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-production")
	if err != nil {
		t.Fatalf("get prod: %v", err)
	}
	if !slices.Equal(prod.Spec.EnvFromSecrets, prodFrom) {
		t.Errorf("production env rewritten by a preview clone delete: %v", prod.Spec.EnvFromSecrets)
	}
	pr, err := s.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-web-pr-7")
	if err != nil {
		t.Fatalf("get preview: %v", err)
	}
	if slices.Contains(pr.Spec.EnvFromSecrets, "alpha-cache-pr-7-conn") {
		t.Errorf("preview still mounts the deleted clone's conn: %v", pr.Spec.EnvFromSecrets)
	}
}
