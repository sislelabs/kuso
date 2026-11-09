package podreason

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSummarizeScheduling(t *testing.T) {
	cases := map[string]string{
		"0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }. preemption: 0/1 nodes are available: 1 Preemption is not helpful for scheduling.": "The node is low on disk, so the pod can't be scheduled",
		"0/3 nodes are available: 3 Insufficient memory.":                               "No node has enough free memory for the pod's request",
		"0/2 nodes are available: 2 node(s) didn't match Pod's node affinity/selector.": "No node matches the placement labels",
		"0/1 nodes are available: 1 node(s) had untolerated taint {dedicated: gpu}.":    "No node accepts the pod (every node is tainted)",
		"something new from a future scheduler":                                         "No node can run the pod right now",
	}
	for in, want := range cases {
		if got := SummarizeScheduling(in); got != want {
			t.Errorf("SummarizeScheduling(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSummarizeImagePull(t *testing.T) {
	// The kubelet flips between ErrImagePull (with the registry reason) and
	// ImagePullBackOff (without it) on every retry; the summary must not flip.
	a := Summarize("ErrImagePull", `rpc error: manifest unknown`)
	b := Summarize("ImagePullBackOff", `Back-off pulling image "x"`)
	if a != b {
		t.Errorf("summary flips across retries: %q vs %q", a, b)
	}
	if a == "" {
		t.Error("empty summary")
	}
}

func pod(created time.Time, mut func(*corev1.Pod)) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", CreationTimestamp: metav1.NewTime(created)}}
	p.Status.Phase = corev1.PodPending
	mut(p)
	return p
}

func TestForPod(t *testing.T) {
	now := time.Now()
	unsched := pod(now.Add(-time.Minute), func(p *corev1.Pod) {
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
			Message: "0/1 nodes are available: 1 node(s) had untolerated taint {node.kubernetes.io/disk-pressure: }."}}
	})
	if is := ForPod(unsched, now); is == nil || is.Kind != KindUnschedulable || is.Summary != "The node is low on disk, so the pod can't be scheduled" {
		t.Fatalf("unschedulable: %+v", is)
	}
	// A fresh not-ready pod is starting, not stuck.
	fresh := pod(now.Add(-2*time.Minute), func(*corev1.Pod) {})
	if is := ForPod(fresh, now); is != nil {
		t.Fatalf("fresh pod flagged: %+v", is)
	}
	stalled := pod(now.Add(-14*time.Minute), func(*corev1.Pod) {})
	if is := ForPod(stalled, now); is == nil || is.Kind != KindRolloutStall {
		t.Fatalf("stalled: %+v", is)
	}
	ready := pod(now.Add(-time.Hour), func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		p.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	})
	if is := ForPod(ready, now); is != nil {
		t.Fatalf("ready pod flagged: %+v", is)
	}
	done := pod(now.Add(-time.Hour), func(p *corev1.Pod) { p.Status.Phase = corev1.PodSucceeded })
	if is := ForPod(done, now); is != nil {
		t.Fatalf("succeeded pod flagged: %+v", is)
	}
	pull := pod(now.Add(-time.Minute), func(p *corev1.Pod) {
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}}}}
	})
	if w := Worst([]*corev1.Pod{stalled, pull}, now); w == nil || w.Kind != KindImagePull {
		t.Fatalf("worst: %+v", w)
	}
}
