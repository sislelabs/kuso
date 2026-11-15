package handlers

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"kuso/server/internal/backup"
)

// OPS-1: server-created backup Jobs run the release-pinned image (or the
// dev override), not a hard-coded :latest.
func TestBackupJobsRunPinnedImage(t *testing.T) {
	c := snapshotWaitContainer("s3", "", "p/a/", time.Now(), ".sql.gz", "", "")
	if c.Image != backup.DefaultImage {
		t.Errorf("image = %q, want backup.DefaultImage %q", c.Image, backup.DefaultImage)
	}
	const dev = "registry.local/kuso-backup:dev"
	t.Setenv("KUSO_BACKUP_IMAGE", dev)
	c = snapshotWaitContainer("s3", "", "p/a/", time.Now(), ".sql.gz", "", "")
	if c.Image != dev || c.ImagePullPolicy != corev1.PullAlways {
		t.Errorf("override: image=%q policy=%s", c.Image, c.ImagePullPolicy)
	}
	const pinned = "ghcr.io/sislelabs/kuso-backup:v1@sha256:0000000000000000000000000000000000000000000000000000000000000000"
	t.Setenv("KUSO_BACKUP_IMAGE", pinned)
	if c = snapshotWaitContainer("s3", "", "p/a/", time.Now(), ".sql.gz", "", ""); c.ImagePullPolicy != corev1.PullIfNotPresent {
		t.Errorf("pinned image policy = %s, want IfNotPresent", c.ImagePullPolicy)
	}
}
