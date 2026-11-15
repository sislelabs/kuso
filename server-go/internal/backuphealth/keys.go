package backuphealth

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

// KeyExportGrace is how long a backup key may stay unexported before it
// fails backup health. A new addon's key is minted with its first backup
// schedule; failing the instant it appears would page on every addon
// create, which teaches operators to ignore this alert.
const KeyExportGrace = 24 * time.Hour

// BackupKeysStatus reports whether the backup encryption keys have been
// escrowed off-cluster. Every dump in the bucket is sealed with one of
// these keys and the cluster holds the only copy, so a lost cluster with
// unexported keys means every off-site backup is unreadable.
type BackupKeysStatus struct {
	// Unexported lists every key Secret ("<namespace>/<secret>") never
	// exported, including ones still inside KeyExportGrace.
	Unexported []string `json:"unexported"`
	// Overdue is the subset that fails health: unexported past the grace.
	Overdue []string `json:"overdue"`
	// ControlPlaneOverdue: the control-plane dump key is among Overdue.
	// Only counted when the control-plane backup is configured, since
	// otherwise no dump is sealed with it.
	ControlPlaneOverdue bool `json:"controlPlaneOverdue"`
	Healthy             bool `json:"healthy"`
	// Complete=false: the Secret list failed; the verdict is unknown.
	Complete bool   `json:"complete"`
	Detail   string `json:"detail"`
}

// ComputeBackupKeys lists the backup-key Secrets and derives the verdict.
// cpBackupConfigured says whether the control-plane CronJob is set up;
// without it the control-plane key seals nothing and is not counted.
func ComputeBackupKeys(ctx context.Context, kc *kube.Client, now time.Time, cpBackupConfigured bool) BackupKeysStatus {
	st := BackupKeysStatus{Unexported: []string{}, Overdue: []string{}}
	if kc == nil || kc.Clientset == nil {
		st.Detail = "backup keys can't be read: no kube client"
		return st
	}
	list, err := kc.Clientset.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: backup.KeyRoleSelector()})
	if err != nil {
		st.Detail = "backup keys can't be read: " + err.Error()
		return st
	}
	st.Complete = true
	for _, s := range list.Items {
		if len(s.Data[backup.KeySecretKey]) == 0 || s.Annotations[backup.KeyExportedAtAnnotation] != "" {
			continue
		}
		id := s.Namespace + "/" + s.Name
		st.Unexported = append(st.Unexported, id)
		cp := s.Name == kube.ControlPlaneBackupKeySecret
		if cp && !cpBackupConfigured {
			continue
		}
		if now.Sub(s.CreationTimestamp.Time) < KeyExportGrace {
			continue
		}
		st.Overdue = append(st.Overdue, id)
		if cp {
			st.ControlPlaneOverdue = true
		}
	}
	sort.Strings(st.Unexported)
	sort.Strings(st.Overdue)
	st.Healthy = len(st.Overdue) == 0
	st.Detail = keysDetail(st)
	return st
}

func keysDetail(st BackupKeysStatus) string {
	switch {
	case len(st.Overdue) > 0:
		what := fmt.Sprintf("%d backup encryption key(s) have never been exported", len(st.Overdue))
		if st.ControlPlaneOverdue {
			what += ", including the control-plane key " + kube.ControlPlaneBackupKeySecret
		}
		return what + ". They exist only in this cluster: if it is lost, every dump they sealed in the bucket is unreadable. " +
			"Run `kuso backup keys export --file <path>` and store the file off-cluster (" + strings.Join(st.Overdue, ", ") + ")."
	case len(st.Unexported) > 0:
		return fmt.Sprintf("%d new backup key(s) not exported yet; export them within %s with `kuso backup keys export --file <path>`.",
			len(st.Unexported), KeyExportGrace)
	default:
		return "Every backup encryption key has been exported."
	}
}

// UnexportedBackupKeys lists ("<namespace>/<secret>") the backup encryption
// key Secrets that have never been exported, regardless of age.
// complete=false when the list failed.
func UnexportedBackupKeys(ctx context.Context, kc *kube.Client) (keys []string, complete bool) {
	st := ComputeBackupKeys(ctx, kc, time.Now(), true)
	if !st.Complete {
		return nil, false
	}
	return st.Unexported, true
}
