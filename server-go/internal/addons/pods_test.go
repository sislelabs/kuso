package addons

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// An addon that renders a Deployment but never runs a pod is invisible today:
// `kuso service pods` only covers services, `kuso health` counts the addon
// healthy because its Helm release deployed fine, and the only symptom is
// connection-refused from whatever tried to use it. ListPods closes that —
// it has to surface WHY a pod isn't running, not just that none are.

func addonPodsService(t *testing.T, pods ...*corev1.Pod) *Service {
	t.Helper()
	s := fakeService(t, seedProj("alpha"))
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	s.Kube.Clientset = kubefake.NewSimpleClientset(objs...)
	return s
}

func TestListAddonPods_SurfacesPendingReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-pooler-abc",
			Namespace: "kuso",
			Labels:    map[string]string{"app.kubernetes.io/instance": "alpha-pg"},
		},
		Status: corev1.PodStatus{
			Phase:  corev1.PodPending,
			Reason: "SchedulingGated",
			Conditions: []corev1.PodCondition{{
				Type:    corev1.PodScheduled,
				Status:  corev1.ConditionFalse,
				Reason:  "Unschedulable",
				Message: `no PriorityClass named "kuso-platform"`,
			}},
		},
	}
	s := addonPodsService(t, pod)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 1 {
		t.Fatalf("pods = %d, want 1", len(out.Pods))
	}
	p := out.Pods[0]
	if p.Phase != "Pending" {
		t.Errorf("phase = %q, want Pending", p.Phase)
	}
	// The whole point: a pod that never starts must explain itself.
	if p.Message == "" {
		t.Error("a Pending pod reported no message — the scheduler's reason is " +
			"the only thing that explains why the addon has no endpoints")
	}
}

func TestListAddonPods_SurfacesWaitingContainerReason(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alpha-pg-pooler-def",
			Namespace: "kuso",
			Labels:    map[string]string{"app.kubernetes.io/instance": "alpha-pg"},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			InitContainerStatuses: []corev1.ContainerStatus{{
				Name: "render-userlist",
				State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{
					Reason:  "CreateContainerConfigError",
					Message: "secret alpha-pg-conn missing key POSTGRES_USER",
				}},
			}},
		},
	}
	s := addonPodsService(t, pod)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 1 {
		t.Fatalf("pods = %d, want 1", len(out.Pods))
	}
	if out.Pods[0].Message == "" {
		t.Error("a stuck init container reported no message — this is the most " +
			"common addon-pod failure and it must not be silent")
	}
}

// Zero pods is itself the answer when a Deployment renders but nothing runs.
func TestListAddonPods_EmptyIsNotAnError(t *testing.T) {
	s := addonPodsService(t)

	out, err := s.ListPods(context.Background(), "alpha", "pg")
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(out.Pods) != 0 {
		t.Errorf("pods = %d, want 0", len(out.Pods))
	}
}
