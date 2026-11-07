package projects

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
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

// `kuso get pods` showed no restart count, so a crashlooping-but-Running
// pod looked healthy.
func TestListPods_SumsRestarts(t *testing.T) {
	t.Parallel()
	env := typedSeed(kube.GVREnvironments, "KusoEnvironment", "alpha-web-production", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso"},
		Spec:       kube.KusoEnvironmentSpec{Project: "alpha", Service: "alpha-web"},
	})
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{}), env)
	s.Kube.Clientset = kubefake.NewSimpleClientset(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production-x", Namespace: "kuso",
			Labels: map[string]string{"app.kubernetes.io/instance": "alpha-web-production"}},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
			{Name: "app", Ready: true, RestartCount: 3},
			{Name: "sidecar", RestartCount: 2},
		}},
	})
	out, err := s.ListPods(context.Background(), "alpha", "web", "production")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Pods) != 1 || out.Pods[0].Restarts != 5 || !out.Pods[0].Ready {
		t.Fatalf("pods = %+v, want one ready pod with 5 restarts", out.Pods)
	}
}
