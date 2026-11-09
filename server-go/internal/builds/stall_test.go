package builds

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func pendingPod(created time.Time, mut func(*corev1.Pod)) corev1.Pod {
	p := corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "b-1", CreationTimestamp: metav1.NewTime(created)}}
	p.Status.Phase = corev1.PodPending
	if mut != nil {
		mut(&p)
	}
	return p
}

func TestStallFor(t *testing.T) {
	now := time.Now()
	started := now.Add(-2 * time.Minute)
	waiting := func(init bool, reason string) func(*corev1.Pod) {
		return func(p *corev1.Pod) {
			cs := corev1.ContainerStatus{Name: "clone", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason, Message: "pull access denied"}}}
			if init {
				p.Status.InitContainerStatuses = []corev1.ContainerStatus{cs}
			} else {
				p.Status.ContainerStatuses = []corev1.ContainerStatus{cs}
			}
		}
	}
	cases := []struct {
		name    string
		pods    []corev1.Pod
		started time.Time
		want    string // kind, "" = not stalled
	}{
		{"too early to tell", []corev1.Pod{pendingPod(now, waiting(true, "ImagePullBackOff"))}, now.Add(-30 * time.Second), ""},
		{"builder image pull", []corev1.Pod{pendingPod(started, waiting(true, "ImagePullBackOff"))}, started, StallImagePull},
		{"main image pull", []corev1.Pod{pendingPod(started, waiting(false, "ErrImagePull"))}, started, StallImagePull},
		{"unschedulable", []corev1.Pod{pendingPod(started, func(p *corev1.Pod) {
			p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/1 nodes are available: 1 Insufficient memory."}}
		})}, started, StallUnschedulable},
		{"cloning in an init container is progress", []corev1.Pod{pendingPod(started, func(p *corev1.Pod) {
			p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "clone", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
		})}, started, ""},
		{"running pod", []corev1.Pod{pendingPod(started, func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning })}, started, ""},
		{"no pod at all", nil, started, StallNoPod},
		{"pending with no reason", []corev1.Pod{pendingPod(started, nil)}, started, StallStarting},
	}
	for _, c := range cases {
		got := StallFor(c.pods, c.started, now)
		kind := ""
		if got != nil {
			kind = got.Kind
		}
		if kind != c.want {
			t.Errorf("%s: kind = %q, want %q (%+v)", c.name, kind, c.want, got)
		}
	}
	if s := StallFor([]corev1.Pod{pendingPod(started, waiting(true, "ImagePullBackOff"))}, started, now); s.Summary != "Builder image can't be pulled" {
		t.Errorf("image pull summary = %q", s.Summary)
	}
	sched := StallFor([]corev1.Pod{pendingPod(started, func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "0/1 nodes are available: 1 Insufficient memory."}}
	})}, started, now)
	if sched.Summary != "Waiting for a node: no node has enough free memory for the pod's request" {
		t.Errorf("unschedulable summary = %q", sched.Summary)
	}
}
