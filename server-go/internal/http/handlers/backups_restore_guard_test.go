package handlers

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
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func restoreJobObj(name string, labels map[string]string, done bool) *batchv1.Job {
	labels["kuso.sislelabs.com/role"] = "restore"
	j := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso-shop", Labels: labels}}
	if done {
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}}
	}
	return j
}

// DB-12: a second restore into a destination with one still running is
// refused; finished restores, other addons and drills don't count.
func TestActiveRestore(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		job  *batchv1.Job
		want string
	}{
		{"running, dest label", restoreJobObj("r1", map[string]string{labelRestoreDest: "shop-db"}, false), "r1"},
		{"running, legacy alias label", restoreJobObj("r2", map[string]string{"kuso.sislelabs.com/addon": "db"}, false), "r2"},
		{"finished", restoreJobObj("r3", map[string]string{labelRestoreDest: "shop-db"}, true), ""},
		{"other addon", restoreJobObj("r4", map[string]string{labelRestoreDest: "shop-cache", "kuso.sislelabs.com/addon": "db"}, false), ""},
	} {
		h := &BackupsHandler{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(tc.job)}}
		got, err := h.activeRestore(context.Background(), "kuso-shop", "shop-db", "db")
		if err != nil || got != tc.want {
			t.Errorf("%s: activeRestore = %q, %v; want %q", tc.name, got, err, tc.want)
		}
	}
}

// DB-9: a restore reads the project's own backup Secret (scoped key) when
// kuso-server rendered one, the shared one otherwise, and always the shared
// one for an instance tenant's dumps.
func TestRestoreBackupSecret(t *testing.T) {
	t.Parallel()
	scoped := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: kube.ProjectBackupSecretName("shop"), Namespace: "kuso-shop"}}
	ctx := context.Background()
	with := &BackupsHandler{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(scoped)}}
	without := &BackupsHandler{Kube: &kube.Client{Clientset: fake.NewSimpleClientset()}}
	if got, _ := with.restoreBackupSecret(ctx, "kuso-shop", "shop", false); got != "kuso-backup-s3-shop" {
		t.Errorf("with scoped secret: %q", got)
	}
	if got, _ := with.restoreBackupSecret(ctx, "kuso-shop", "shop", true); got != backupSecretName {
		t.Errorf("instance tenant: %q", got)
	}
	if got, _ := without.restoreBackupSecret(ctx, "kuso-shop", "shop", false); got != backupSecretName {
		t.Errorf("without scoped secret: %q", got)
	}
}

// DB-9: the instance key copy is owned by the restore Job from creation,
// and the Job requires it (a keyless start would refuse the encrypted dump).
func TestRestoreKeyCopy_OwnedAtCreation(t *testing.T) {
	t.Parallel()
	cs := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-pg-backup-key", Namespace: "kuso"},
		Data:       map[string][]byte{"key": []byte("k3y")},
	})
	h := &BackupsHandler{Kube: &kube.Client{Clientset: cs}}
	loc := &instanceTenantBackupLoc{keyNS: "kuso", keyName: "kuso-instance-pg-backup-key"}
	env, val, err := h.restoreKeyEnv(context.Background(), "kuso-shop", "shop-db-restore-1", "shop", "kuso-instance/x/1.sql.gz", loc)
	if err != nil || val != "k3y" {
		t.Fatalf("restoreKeyEnv = %q, %v", val, err)
	}
	if ref := env.ValueFrom.SecretKeyRef; ref.Name != "shop-db-restore-1-key" || (ref.Optional != nil && *ref.Optional) {
		t.Errorf("key env must require the copy: %+v", ref)
	}
	if _, err := cs.CoreV1().Secrets("kuso-shop").Get(context.Background(), "shop-db-restore-1-key", metav1.GetOptions{}); err == nil {
		t.Fatal("key copied before the Job exists")
	}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "shop-db-restore-1", Namespace: "kuso-shop", UID: "uid-1"}}
	if err := h.createRestoreKeyCopy(context.Background(), job, "shop", val); err != nil {
		t.Fatal(err)
	}
	sec, err := cs.CoreV1().Secrets("kuso-shop").Get(context.Background(), "shop-db-restore-1-key", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(sec.OwnerReferences) != 1 || sec.OwnerReferences[0].UID != "uid-1" || sec.OwnerReferences[0].Kind != "Job" {
		t.Errorf("owner refs = %+v", sec.OwnerReferences)
	}
}

// DB-12: the pre-restore snapshot is a one-off run of the destination's
// backup CronJob; without one the restore proceeds with a warning.
func TestStartPreRestoreSnapshot(t *testing.T) {
	t.Parallel()
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "shop-db-backup", Namespace: "kuso-shop", UID: "cj-uid"}}
	cj.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{
		Name: "backup", Env: []corev1.EnvVar{{Name: "RETENTION_DAYS", Value: "7"}},
	}}
	pg := &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: "shop-db"}, Spec: kube.KusoAddonSpec{Kind: "postgres"}}
	ctx := context.Background()

	h := &BackupsHandler{Kube: &kube.Client{Clientset: fake.NewSimpleClientset(cj)}}
	job, since, warn, err := h.startPreRestoreSnapshot(ctx, "kuso-shop", pg)
	if err != nil || job == nil || warn != "" || since.IsZero() {
		t.Fatalf("with schedule: job=%v warn=%q err=%v", job, warn, err)
	}
	if len(job.OwnerReferences) != 1 || job.OwnerReferences[0].UID != "cj-uid" || len(job.Name) > 63 {
		t.Errorf("snapshot job = %s %+v", job.Name, job.OwnerReferences)
	}
	// DB-3: the snapshot must not prune, or it can delete the dump being restored.
	if got := job.Spec.Template.Spec.Containers[0].Env[0].Value; got != "0" {
		t.Errorf("snapshot RETENTION_DAYS = %q, want 0 (no prune)", got)
	}
	if got := cj.Spec.JobTemplate.Spec.Template.Spec.Containers[0].Env[0].Value; got != "7" {
		t.Errorf("CronJob template mutated: RETENTION_DAYS = %q", got)
	}

	h = &BackupsHandler{Kube: &kube.Client{Clientset: fake.NewSimpleClientset()}}
	if job, _, warn, err := h.startPreRestoreSnapshot(ctx, "kuso-shop", pg); err != nil || job != nil || warn == "" {
		t.Errorf("no schedule: job=%v warn=%q err=%v", job, warn, err)
	}
	s3 := &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: "shop-files"}, Spec: kube.KusoAddonSpec{Kind: "s3"}}
	if job, _, warn, _ := h.startPreRestoreSnapshot(ctx, "kuso-shop", s3); job != nil || warn == "" {
		t.Errorf("s3: job=%v warn=%q", job, warn)
	}
}

// The restore's init container only lets the restore run once a manifest
// stamped at or after the request has landed: an older one, or a newer
// dump whose manifest isn't uploaded yet, keeps it waiting.
func TestSnapshotWaitShell(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	c := snapshotWaitContainer("kuso-backup-s3", "", "shop/shop-db/", since)
	for _, tc := range []struct {
		name    string
		listing string
		ok      bool
	}{
		{"older manifest only", "2026-11-11 03:00:01   1234 20261111T030000Z.sql.gz.manifest.json\n2026-11-11 03:00:01   99 20261111T030000Z.sql.gz", false},
		{"new dump, manifest pending", "2026-11-11 10:00:05   99 20261111T100001Z.sql.gz", false},
		{"new manifest", "2026-11-11 10:00:06   1234 20261111T100001Z.sql.gz.manifest.json", true},
		{"same second", "2026-11-11 10:00:06   1234 20261111T100000Z.archive.gz.manifest.json", true},
	} {
		dir := t.TempDir()
		stub := "#!/bin/sh\ncat " + filepath.Join(dir, "ls") + "\n"
		if err := os.WriteFile(filepath.Join(dir, "ls"), []byte(tc.listing+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "aws"), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sh", "-c", c.Args[0])
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "BUCKET=b", "S3_ENDPOINT=http://x"}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
			}
		}
		cmd.Env = append(cmd.Env, "SNAP_WAIT_SECONDS=0")
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v\n%s", tc.name, err, tc.ok, out)
		}
	}
	if !strings.Contains(strings.Join(func() []string {
		var s []string
		for _, e := range c.Env {
			s = append(s, e.Name+"="+e.Value)
		}
		return s
	}(), " "), "SNAP_SINCE=20261111T100000Z") {
		t.Errorf("SNAP_SINCE not stamped in backup-key format: %+v", c.Env)
	}
}

func TestBuildRestoreJob_ReapedWithItsKeyCopy(t *testing.T) {
	t.Parallel()
	job := buildRestoreJob("kuso-shop", "shop-db-restore-1", "shop", "db", "db", "true", nil)
	if job.Spec.TTLSecondsAfterFinished == nil {
		t.Error("restore Job has no TTL, so its owned key copy is never garbage-collected")
	}
}
