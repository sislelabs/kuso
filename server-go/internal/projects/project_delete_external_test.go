package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// Project delete must remove an external addon's credential Secrets the
// same way addon delete does: the mirrored <addon>-conn and the
// kuso-created <addon>-external source (plaintext credentials). Neither
// carries the project label, so the label sweep never reaches them. A
// source Secret the user adopted (no external-source=true) stays.
func TestDeleteProject_RemovesExternalAddonSecrets(t *testing.T) {
	t.Parallel()
	const ns = "kuso"
	ext := typedSeed(kube.GVRAddons, "KusoAddon", "beta-ext", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "beta-ext", Namespace: ns, Labels: map[string]string{labelProject: "beta"}},
		Spec:       kube.KusoAddonSpec{Project: "beta", Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "beta-ext-external"}},
	})
	adopted := typedSeed(kube.GVRAddons, "KusoAddon", "beta-adopted", &kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: "beta-adopted", Namespace: ns, Labels: map[string]string{labelProject: "beta"}},
		Spec:       kube.KusoAddonSpec{Project: "beta", Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "users-own-secret"}},
	})
	proj := seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}})

	sec := func(name string, labels map[string]string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: labels}}
	}
	s, _, cs := newCascadeFixture(t, []seed{proj, ext, adopted},
		sec("beta-ext-external", map[string]string{"kuso.sislelabs.com/external-source": "true", "kuso.sislelabs.com/addon": "beta-ext"}),
		sec("beta-ext-conn", map[string]string{"kuso.sislelabs.com/addon-conn": "true", "kuso.sislelabs.com/external": "true"}),
		sec("users-own-secret", nil),
		sec("beta-adopted-conn", map[string]string{"kuso.sislelabs.com/addon-conn": "true", "kuso.sislelabs.com/external": "true"}),
	)

	if err := s.Delete(context.Background(), "beta"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	for _, gone := range []string{"beta-ext-external", "beta-ext-conn", "beta-adopted-conn"} {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), gone, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("secret %s survived project delete (err=%v)", gone, err)
		}
	}
	if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), "users-own-secret", metav1.GetOptions{}); err != nil {
		t.Errorf("user-adopted source secret was deleted: %v", err)
	}
}
