package projects

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func crashPod(name, rs string, cs corev1.ContainerStatus) corev1.Pod {
	ctrl := true
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:            name,
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs, Controller: &ctrl}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{cs}},
	}
}

func lastExit(code int32, reason string, at time.Time, restarts int32) corev1.ContainerStatus {
	return corev1.ContainerStatus{
		Name:         "app",
		RestartCount: restarts,
		State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
		LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
			ExitCode: code, Reason: reason, FinishedAt: metav1.NewTime(at),
		}},
	}
}

func TestPickCrash(t *testing.T) {
	t.Parallel()
	now := time.Now()
	healthy := crashPod("ok", "new", corev1.ContainerStatus{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}})

	if got := pickCrash([]corev1.Pod{healthy}, "new"); got != nil {
		t.Fatalf("healthy pods: got %+v, want nil", got)
	}

	// A draining old pod's newer exit loses to the current ReplicaSet's crash.
	old := crashPod("old", "prev", lastExit(1, "Error", now, 2))
	cur := crashPod("cur", "new", lastExit(1, "Error", now.Add(-time.Minute), 3))
	got := pickCrash([]corev1.Pod{old, cur}, "new")
	if got == nil || got.pod.Name != "cur" || !got.previous || got.restarts != 3 {
		t.Fatalf("got %+v, want the current pod's previous run", got)
	}

	// Among current pods a non-zero exit wins over a newer clean exit.
	clean := crashPod("clean", "new", lastExit(0, "Completed", now, 1))
	if got := pickCrash([]corev1.Pod{clean, cur}, "new"); got == nil || got.pod.Name != "cur" {
		t.Fatalf("got %+v, want the failing exit", got)
	}

	// Same rank: the newest termination wins.
	newer := crashPod("newer", "new", lastExit(137, "OOMKilled", now, 1))
	if got := pickCrash([]corev1.Pod{cur, newer}, "new"); got == nil || got.pod.Name != "newer" || got.term.Reason != "OOMKilled" {
		t.Fatalf("got %+v, want the newest exit", got)
	}

	// A container terminated right now (not yet restarted) reads its
	// current logs, not the previous ones.
	dead := crashPod("dead", "new", corev1.ContainerStatus{Name: "app", State: corev1.ContainerState{
		Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Reason: "Error", FinishedAt: metav1.NewTime(now)},
	}})
	if got := pickCrash([]corev1.Pod{dead}, "new"); got == nil || got.previous {
		t.Fatalf("got %+v, want current-container logs", got)
	}

	// Release-hook Job pods aren't the service's crash.
	ctrl := true
	job := crashPod("hook", "", lastExit(1, "Error", now, 1))
	job.OwnerReferences = []metav1.OwnerReference{{Kind: "Job", Name: "hook", Controller: &ctrl}}
	if got := pickCrash([]corev1.Pod{job}, "new"); got != nil {
		t.Fatalf("job pod: got %+v, want nil", got)
	}
}
