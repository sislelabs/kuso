package handlers

import (
	"strings"
	"testing"
)

// The restore pod must be inside the project netpol (project label) to
// reach the destination DB, and needs public egress for the S3 artifact —
// the same pair the chart's scheduled backup CronJob pod carries.
func TestBuildRestoreJob_PodTemplateLabels(t *testing.T) {
	t.Parallel()

	job := buildRestoreJob("kuso-e2e", "e2e-db-restore-1", "e2e", "db", "db", "echo restore", nil)
	labels := job.Spec.Template.Labels
	if got := labels["kuso.sislelabs.com/project"]; got != "e2e" {
		t.Errorf("pod template kuso.sislelabs.com/project = %q, want e2e", got)
	}
	if got := labels["kuso.sislelabs.com/network-egress-public"]; got != "true" {
		t.Errorf("pod template network-egress-public = %q, want true", got)
	}
	if job.Namespace != "kuso-e2e" || job.Labels["kuso.sislelabs.com/addon"] != "db" {
		t.Errorf("job meta = ns %q labels %v", job.Namespace, job.Labels)
	}
}

func TestBuildRestoreJob_PostgresWaitsBeforeConnecting(t *testing.T) {
	t.Parallel()

	script, err := restoreScriptForKind("postgres")
	if err != nil {
		t.Fatal(err)
	}
	job := buildRestoreJob("kuso-e2e", "e2e-db-restore-1", "e2e", "db", "db", script, nil)
	args := job.Spec.Template.Spec.Containers[0].Args[0]
	wait := strings.Index(args, "pg_isready")
	apply := strings.Index(args, "psql -v ON_ERROR_STOP=1")
	if wait < 0 || wait > apply {
		t.Fatalf("restore Job connects before waiting for postgres (pg_isready at %d, psql at %d)", wait, apply)
	}
	if !strings.Contains(args, "until aws s3 cp") {
		t.Error("restore Job's S3 download has no retry")
	}
}
