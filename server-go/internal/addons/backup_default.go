package addons

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

// DefaultBackupSchedule is the schedule a new database addon gets when the
// create request doesn't say otherwise. Same value instancepg gives the
// managed instance server.
const DefaultBackupSchedule = "0 3 * * *"

// defaultBackupKinds are the datastores whose loss is loss of user data.
// redis/s3 have producers too but are commonly caches or have their own
// replication story, so they stay opt-in.
var defaultBackupKinds = map[string]bool{"postgres": true, "mysql": true, "mongodb": true}

// validateCreateBackup checks an explicit create-time schedule. "" is a
// deliberate opt-out and always valid.
func validateCreateBackup(req CreateAddonRequest) error {
	if req.BackupSchedule == nil {
		return nil
	}
	s := strings.TrimSpace(*req.BackupSchedule)
	if s == "" {
		return nil
	}
	if !cronExpr5.MatchString(s) {
		return fmt.Errorf("%w: backup schedule %q must be a 5-field cron expression (e.g. `0 3 * * *`)", ErrInvalid, s)
	}
	if !kube.AddonKindHasBackupProducer(req.Kind) {
		return fmt.Errorf("%w: kuso has no backup producer for kind %s, so a backup schedule would never run; back it up outside kuso", ErrInvalid, req.Kind)
	}
	return nil
}

// createBackup decides spec.backup for a new addon. An explicit schedule
// wins (including "" = no backups). Otherwise a native, non-clone database
// addon is scheduled by default once instance backups are configured;
// without the kuso-backup-s3 Secret every run would no-op.
//
// Clones (preview/env copies, which carry ExtraLabels/ExtraAnnotations) are
// excluded: they hold a copy of production data that the source's own
// backup already covers, and dumping them would multiply that copy in the
// bucket.
func (s *Service) createBackup(ctx context.Context, req CreateAddonRequest) *kube.KusoBackup {
	if req.BackupSchedule != nil {
		sched := strings.TrimSpace(*req.BackupSchedule)
		if sched == "" {
			return nil
		}
		return &kube.KusoBackup{Schedule: sched, RetentionDays: DefaultBackupRetentionDays}
	}
	if !defaultBackupKinds[req.Kind] || req.UseInstanceAddon != "" ||
		len(req.ExtraLabels) > 0 || len(req.ExtraAnnotations) > 0 {
		return nil
	}
	if req.External != nil && (req.External.SecretName != "" || len(req.External.SecretKeys) > 0) {
		return nil
	}
	if s.Kube == nil || s.Kube.Clientset == nil {
		return nil
	}
	if _, err := s.Kube.Clientset.CoreV1().Secrets(s.Namespace).Get(ctx, kube.BackupSecretName, metav1.GetOptions{}); err != nil {
		if !apierrors.IsNotFound(err) {
			slog.Default().Warn("addons: backup settings unreadable; new addon left without a default backup schedule", "kind", req.Kind, "err", err)
		}
		return nil
	}
	return &kube.KusoBackup{Schedule: DefaultBackupSchedule, RetentionDays: DefaultBackupRetentionDays}
}
