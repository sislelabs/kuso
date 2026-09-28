package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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
	// NOT t.Parallel(): this test mutates the package-level
	// snapshotPollInterval/snapshotPollTimeout knobs, which the other
	// waitForJob tests read concurrently. Running it in parallel is a
	// genuine data race (caught by `go test -race`), not a false
	// positive — the race detector flagged it against
	// TestWaitForJob_ContextCancelled, which mutates the same globals.
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
	// NOT t.Parallel() — mutates the package-level snapshotPollInterval.
	// See TestWaitForJob_TimesOut above.
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

// A fresh pod in a project namespace can't reach the DB for ~5-20s until
// kube-router syncs the NetworkPolicy for its IP. The snapshot Job runs with
// BackoffLimit 0 and gates the migration, so it must wait, bounded, before
// pg_dump's first connect.
func TestBuildSnapshotJob_WaitsForPostgresBeforeDump(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso-e2e", "e2e-db-snapshot-1", "e2e/e2e-db/k.sql.gz", "e2e", "db", "pre-deploy", "abc", "")
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	wait := strings.Index(script, `pg_isready -h "${POSTGRES_HOST}"`)
	dump := strings.Index(script, "pg_dump ")
	if wait < 0 || dump < 0 || wait > dump {
		t.Fatalf("pg_isready wait (at %d) must precede pg_dump (at %d):\n%s", wait, dump, script)
	}
	if !strings.Contains(script[:dump], "exit 1") {
		t.Error("wait loop is not bounded (no exit 1 before pg_dump)")
	}
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Errorf("snapshot script does not parse: %v\n%s", err, out)
	}
}

// Without the project label the pod sits outside the project netpols, so
// the addon's default-deny ingress never admits it; without public egress
// the S3 upload is blocked. Same pair the restore Job and backup CronJob carry.
func TestBuildSnapshotJob_PodInsideProjectNetpol(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso-e2e", "e2e-db-snapshot-1", "k", "e2e", "db", "t", "b", "")
	labels := job.Spec.Template.Labels
	if got := labels["kuso.sislelabs.com/project"]; got != "e2e" {
		t.Errorf("pod label project = %q, want e2e", got)
	}
	if got := labels["kuso.sislelabs.com/network-egress-public"]; got != "true" {
		t.Errorf("pod label network-egress-public = %q, want true", got)
	}
}

// The snapshot is the rollback point for a failed migration, restored into
// the same server. The image's default pg_dump is 18, which writes SET
// transaction_timeout; restoring that into PG16 aborts under ON_ERROR_STOP,
// so the rollback point would be unusable exactly when it is needed. The
// client must match the server major, and a failed probe must fail the Job
// rather than dump blind.
func TestBuildSnapshotJob_UsesVersionMatchedClient(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso-e2e", "e2e-db-snapshot-1", "e2e/e2e-db/k.sql.gz", "e2e", "db", "pre-deploy", "abc", "")
	script := job.Spec.Template.Spec.Containers[0].Args[0]
	if out, err := exec.Command("sh", "-n", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("snapshot script does not parse: %v\n%s", err, out)
	}
	cases := []struct {
		name      string
		psql      string
		wantDump  string // version whose pg_dump ran; "" = none
		wantInOut string
	}{
		{"pg16 server", "echo 160004", "16", "server major=16"},
		{"pg17 server", "echo 170002", "17", "server major=17"},
		{"probe fails", `echo "psql: auth failed" >&2; exit 2`, "", "refusing to pick a pg_dump client"},
		{"probe garbage", "echo nope", "", "unparseable server_version_num"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, dumped, err := runSnapshotScript(t, script, tc.psql)
			if dumped != tc.wantDump {
				t.Errorf("pg_dump version run = %q, want %q:\n%s", dumped, tc.wantDump, out)
			}
			if tc.wantDump == "" && err == nil {
				t.Errorf("script exited 0 without dumping:\n%s", out)
			}
			if tc.wantDump != "" && err != nil {
				t.Errorf("script failed: %v\n%s", err, out)
			}
			if !strings.Contains(out, tc.wantInOut) {
				t.Errorf("output missing %q:\n%s", tc.wantInOut, out)
			}
		})
	}
}

// runSnapshotScript runs the snapshot script with the image's
// /usr/libexec/postgresql<major>/ layout recreated under a temp dir. Every
// version's psql answers the probe with psqlBody; each pg_dump records its
// version so the test can see which client was picked.
func runSnapshotScript(t *testing.T, script, psqlBody string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "libexec")
	marker := filepath.Join(dir, "dumped")
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []string{"16", "17", "18"} {
		write(filepath.Join(root, "postgresql"+v, "psql"), psqlBody)
		write(filepath.Join(root, "postgresql"+v, "pg_dump"), "printf "+v+" > "+marker+"; echo 'select 1;'")
	}
	bin := filepath.Join(dir, "bin")
	write(filepath.Join(bin, "pg_isready"), "exit 0")
	write(filepath.Join(bin, "pg_dump"), "printf default > "+marker+"; echo 'select 1;'")
	write(filepath.Join(bin, "aws"), "exit 0")
	write(filepath.Join(bin, "sleep"), "exit 0")

	script = strings.ReplaceAll(script, "/usr/libexec/postgresql", filepath.Join(root, "postgresql"))
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"BUCKET=b", "S3_ENDPOINT=e", "KEY=e2e/e2e-db/k.sql.gz",
		"POSTGRES_HOST=e2e-db", "POSTGRES_USER=kuso", "POSTGRES_DB=e2e", "POSTGRES_PASSWORD=pw",
	}
	out, err := cmd.CombinedOutput()
	got, _ := os.ReadFile(marker)
	return string(out), string(got), err
}

// Finished snapshot Jobs (and their pods) had no TTL, so every deploy of a
// snapshotBeforeDeploy service left one behind forever (live, e2e3).
func TestBuildSnapshotJob_HasTTL(t *testing.T) {
	t.Parallel()
	job := buildSnapshotJob("kuso", "p-db-snapshot-1", "k", "p", "db", "t", "b", "")
	if ttl := job.Spec.TTLSecondsAfterFinished; ttl == nil || *ttl <= 0 {
		t.Fatalf("snapshot Job has no TTL (%v); finished Jobs pile up", ttl)
	}
}
