package builds

import (
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// A clone failure was classified from cache-init's disk-usage output,
// because the first container with any log output won.
func TestBuildLogContainerOrder_FailedContainerFirst(t *testing.T) {
	t.Parallel()
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			InitContainers: []corev1.Container{{Name: "cache-init"}, {Name: "clone"}, {Name: "env-detect"}},
			Containers:     []corev1.Container{{Name: "build"}},
		},
		Status: corev1.PodStatus{
			InitContainerStatuses: []corev1.ContainerStatus{
				{Name: "cache-init", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 0}}},
				{Name: "clone", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 128}}},
			},
		},
	}
	got := buildLogContainerOrder(pod)
	want := []string{"clone", "build", "cache-init", "env-detect"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}
