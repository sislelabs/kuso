package backup

import (
	"os"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// DefaultImage is the kuso-backup image every backup, restore, drill,
// snapshot and preview-seed Job runs. Those Jobs receive DB passwords,
// the S3 credentials and the backup keys, so a release pins it:
// hack/release.sh rewrites this line to the release's tag@digest on every
// ship (and the kusoaddon chart's default to match). The moving :latest
// tag is only the value in an unreleased tree.
const DefaultImage = "ghcr.io/sislelabs/kuso-backup:v0.50.0@sha256:c7c9d0c49e79cda3a42bf41cb1be80cb1661b47e9ddecb94c295d44b1a1b3a93"

// Image is DefaultImage unless KUSO_BACKUP_IMAGE overrides it (dev
// clusters testing an unreleased build/backup).
func Image() string {
	if v := strings.TrimSpace(os.Getenv("KUSO_BACKUP_IMAGE")); v != "" {
		return v
	}
	return DefaultImage
}

// ImagePullPolicy: a digest-pinned image never changes, so a cached copy
// is the right one; a tag has to be re-pulled or a node keeps whatever it
// cached first.
func ImagePullPolicy(image string) corev1.PullPolicy {
	if strings.Contains(image, "@sha256:") {
		return corev1.PullIfNotPresent
	}
	return corev1.PullAlways
}
