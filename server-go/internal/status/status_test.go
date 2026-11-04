package status

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// release.json pins images as repo:tag@sha256:…; the operator version
// must still be the tag, not the digest tail.
func TestFetchOperatorVersion_DigestPinnedImage(t *testing.T) {
	t.Parallel()
	for img, want := range map[string]string{
		"ghcr.io/sislelabs/kuso-operator:v0.32.4":                 "v0.32.4",
		"ghcr.io/sislelabs/kuso-operator:v0.32.4@sha256:0123abcd": "v0.32.4",
	} {
		cs := fake.NewSimpleClientset(&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-operator-controller-manager-x", Namespace: "kuso-operator-system"},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Image: img}}},
		})
		s := &Service{Kube: &kube.Client{Clientset: cs}}
		if got := s.fetchOperatorVersion(context.Background()); got != want {
			t.Errorf("%s: got %q, want %q", img, got, want)
		}
	}
}
