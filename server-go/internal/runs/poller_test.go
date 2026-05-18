package runs

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
)

// TestIsTerminal pins the contract the tick loop relies on: any
// phase that means "no more observations needed" returns true.
// Future additions (e.g. a "timed-out" terminal state) must be
// reflected here AND in the markFailed paths.
func TestIsTerminal(t *testing.T) {
	terminal := []string{"succeeded", "failed", "cancelled"}
	inflight := []string{"", "pending", "running", "unknown", "weird"}
	for _, p := range terminal {
		if !isTerminal(p) {
			t.Errorf("isTerminal(%q) = false, want true", p)
		}
	}
	for _, p := range inflight {
		if isTerminal(p) {
			t.Errorf("isTerminal(%q) = true, want false", p)
		}
	}
}

// TestJobTerminalCondition validates the picker that drives
// markSucceeded vs markFailed. We construct synthetic Jobs with
// every relevant condition combination and verify the right one
// (or none) is returned.
func TestJobTerminalCondition(t *testing.T) {
	cases := []struct {
		name     string
		conds    []batchv1.JobCondition
		wantType batchv1.JobConditionType // "" when nil expected
	}{
		{
			name:     "no conditions → nil",
			conds:    nil,
			wantType: "",
		},
		{
			name: "only suspended-ish condition → nil",
			conds: []batchv1.JobCondition{
				{Type: batchv1.JobSuspended, Status: corev1.ConditionTrue},
			},
			wantType: "",
		},
		{
			name: "complete=True → JobComplete",
			conds: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
			wantType: batchv1.JobComplete,
		},
		{
			name: "failed=True → JobFailed",
			conds: []batchv1.JobCondition{
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Message: "ImagePullBackOff"},
			},
			wantType: batchv1.JobFailed,
		},
		{
			name: "complete=False is not terminal",
			conds: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionFalse},
			},
			wantType: "",
		},
		{
			name: "both set → first true match wins (Complete listed first)",
			conds: []batchv1.JobCondition{
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
			wantType: batchv1.JobComplete,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: tc.conds}}
			got := jobTerminalCondition(job)
			if tc.wantType == "" {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected condition %s, got nil", tc.wantType)
			}
			if got.Type != tc.wantType {
				t.Errorf("got %s, want %s", got.Type, tc.wantType)
			}
		})
	}
}
