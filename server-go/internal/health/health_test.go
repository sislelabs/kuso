package health

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// TestPodTerminatedSignal: OOMKilled lives in the Terminated state (current
// or last), not in the Waiting reason — so the health watcher must read it
// from there to feed failures.Classify a KindOOM-worthy signal instead of
// the CrashLoopBackOff the pod shows on restart.
func TestPodTerminatedSignal(t *testing.T) {
	term := func(reason string, code int32) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			State: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code},
			},
		}
	}
	lastTerm := func(reason string, code int32) corev1.ContainerStatus {
		return corev1.ContainerStatus{
			// Current state is Waiting (CrashLoopBackOff) — the OOM is in
			// the LAST termination, which is exactly the case the old code
			// missed.
			State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			LastTerminationState: corev1.ContainerState{
				Terminated: &corev1.ContainerStateTerminated{Reason: reason, ExitCode: code},
			},
		}
	}
	cases := []struct {
		name       string
		statuses   []corev1.ContainerStatus
		wantReason string
		wantCode   int32
	}{
		{"oom in current terminated", []corev1.ContainerStatus{term("OOMKilled", 137)}, "OOMKilled", 137},
		{"oom in last termination behind crashloop", []corev1.ContainerStatus{lastTerm("OOMKilled", 137)}, "OOMKilled", 137},
		{"clean exit ignored", []corev1.ContainerStatus{term("Completed", 0)}, "", 0},
		{"no termination", []corev1.ContainerStatus{{State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod := &corev1.Pod{Status: corev1.PodStatus{ContainerStatuses: tc.statuses}}
			gotR, gotC := podTerminatedSignal(pod)
			if gotR != tc.wantReason || gotC != tc.wantCode {
				t.Errorf("got (%q,%d), want (%q,%d)", gotR, gotC, tc.wantReason, tc.wantCode)
			}
		})
	}
}
