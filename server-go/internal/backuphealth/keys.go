package backuphealth

import (
	"context"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

// UnexportedBackupKeys lists ("<namespace>/<secret>") the backup encryption
// key Secrets that have never been exported. Each is the only copy of the
// key its addon's encrypted dumps need, so losing the cluster loses those
// dumps too. complete=false when the list failed. Informational: it never
// makes anything unhealthy.
func UnexportedBackupKeys(ctx context.Context, kc *kube.Client) (keys []string, complete bool) {
	if kc == nil || kc.Clientset == nil {
		return nil, false
	}
	list, err := kc.Clientset.CoreV1().Secrets(metav1.NamespaceAll).List(ctx, metav1.ListOptions{LabelSelector: backup.KeyRoleSelector()})
	if err != nil {
		return nil, false
	}
	for _, s := range list.Items {
		if len(s.Data[backup.KeySecretKey]) == 0 || s.Annotations[backup.KeyExportedAtAnnotation] != "" {
			continue
		}
		keys = append(keys, s.Namespace+"/"+s.Name)
	}
	sort.Strings(keys)
	return keys, true
}
