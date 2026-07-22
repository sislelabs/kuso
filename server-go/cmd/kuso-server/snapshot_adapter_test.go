package main

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func TestSnapshotJobPhase(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		conds []batchv1.JobCondition
		want  jobPhase
	}{
		{"no conditions is running", nil, jobRunning},
		{"complete true", []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}, jobComplete},
		{"failed true", []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}, jobFailed},
		// A condition present but False must not be treated as terminal.
		{"complete false is running", []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}}, jobRunning},
		{"failed false is running", []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionFalse}}, jobRunning},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			job := &batchv1.Job{Status: batchv1.JobStatus{Conditions: tc.conds}}
			if got := snapshotJobPhase(job); got != tc.want {
				t.Errorf("snapshotJobPhase = %v, want %v", got, tc.want)
			}
		})
	}
}

func newTestAdapter(cs *kubefake.Clientset) *snapshotAdapter {
	return &snapshotAdapter{kc: &kube.Client{Clientset: cs}, homeNS: "kuso"}
}

func seedJob(t *testing.T, cs *kubefake.Clientset, ns, name string, conds []batchv1.JobCondition) {
	t.Helper()
	_, err := cs.BatchV1().Jobs(ns).Create(context.Background(), &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Status:     batchv1.JobStatus{Conditions: conds},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("seed job: %v", err)
	}
}

func TestWaitForJob_CompletesImmediately(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset()
	seedJob(t, cs, "kuso", "snap-1", []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}})
	a := newTestAdapter(cs)
	if err := a.waitForJob(context.Background(), "kuso", "snap-1"); err != nil {
		t.Fatalf("waitForJob on complete job: %v", err)
	}
}

func TestWaitForJob_FailedIsError(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset()
	seedJob(t, cs, "kuso", "snap-2", []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}})
	a := newTestAdapter(cs)
	err := a.waitForJob(context.Background(), "kuso", "snap-2")
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("waitForJob on failed job = %v, want failure error", err)
	}
}

func TestWaitForJob_TimesOut(t *testing.T) {
	t.Parallel()
	// A job that never reaches a terminal condition must surface a timeout
	// error, NOT return nil — otherwise the migration would proceed against an
	// unfinished snapshot. Shrink the poll bounds so the test is fast.
	oldInterval, oldTimeout := snapshotPollInterval, snapshotPollTimeout
	snapshotPollInterval = 5 * time.Millisecond
	snapshotPollTimeout = 20 * time.Millisecond
	defer func() { snapshotPollInterval, snapshotPollTimeout = oldInterval, oldTimeout }()

	cs := kubefake.NewSimpleClientset()
	seedJob(t, cs, "kuso", "snap-3", nil) // no conditions → never terminal
	a := newTestAdapter(cs)
	err := a.waitForJob(context.Background(), "kuso", "snap-3")
	if err == nil || !strings.Contains(err.Error(), "did not complete") {
		t.Fatalf("waitForJob on stuck job = %v, want timeout error", err)
	}
}

func TestWaitForJob_MissingJobIsError(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset()
	a := newTestAdapter(cs)
	if err := a.waitForJob(context.Background(), "kuso", "nope"); err == nil {
		t.Fatal("waitForJob on missing job = nil, want error")
	}
}

func TestWaitForJob_ContextCancelled(t *testing.T) {
	t.Parallel()
	oldInterval := snapshotPollInterval
	snapshotPollInterval = 50 * time.Millisecond
	defer func() { snapshotPollInterval = oldInterval }()

	cs := kubefake.NewSimpleClientset()
	seedJob(t, cs, "kuso", "snap-4", nil) // running, never terminal
	a := newTestAdapter(cs)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before first sleep completes
	if err := a.waitForJob(ctx, "kuso", "snap-4"); err == nil {
		t.Fatal("waitForJob with cancelled ctx = nil, want error")
	}
}

func TestMirrorBackupSecret_MissingSourceIsError(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset() // no kuso-backup-s3 secret anywhere
	a := newTestAdapter(cs)
	err := a.mirrorBackupSecret(context.Background(), "proj-ns")
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("mirrorBackupSecret with no source = %v, want not-configured error", err)
	}
}

func TestMirrorBackupSecret_CopiesIntoTargetNS(t *testing.T) {
	t.Parallel()
	cs := kubefake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: snapshotBackupSecretName, Namespace: "kuso"},
		Data:       map[string][]byte{"bucket": []byte("b"), "endpoint": []byte("e")},
	})
	a := newTestAdapter(cs)
	if err := a.mirrorBackupSecret(context.Background(), "proj-ns"); err != nil {
		t.Fatalf("mirrorBackupSecret: %v", err)
	}
	got, err := cs.CoreV1().Secrets("proj-ns").Get(context.Background(), snapshotBackupSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("mirrored secret not found in target ns: %v", err)
	}
	if string(got.Data["bucket"]) != "b" || string(got.Data["endpoint"]) != "e" {
		t.Errorf("mirrored secret data wrong: %v", got.Data)
	}
	// Idempotent: a second call updates in place without error.
	if err := a.mirrorBackupSecret(context.Background(), "proj-ns"); err != nil {
		t.Fatalf("mirrorBackupSecret second call: %v", err)
	}
}
