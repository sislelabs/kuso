package projects

import (
	"context"
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// An env CR with an empty spec.project/spec.service (legacy or hand-edited)
// must not be reachable through another project's ?env= — this list feeds
// the browser terminal.
func TestListPods_RejectsEnvWithEmptyOwnership(t *testing.T) {
	t.Parallel()
	orphan := typedSeed(kube.GVREnvironments, "KusoEnvironment", "beta-web-production", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "beta-web-production", Namespace: "kuso"},
	})
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}), orphan)
	s.Kube.Clientset = kubefake.NewSimpleClientset()

	if _, err := s.ListPods(context.Background(), "alpha", "web", "beta-web-production"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListPods on foreign env with empty spec: err = %v, want ErrNotFound", err)
	}
}
