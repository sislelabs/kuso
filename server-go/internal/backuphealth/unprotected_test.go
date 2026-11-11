package backuphealth

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"kuso/server/internal/kube"
)

// A tenant created after the instance's last good run has no dump of its
// own yet; inheriting the instance's "ok" hid that.
func TestComputeAddons_InstanceTenantPendingFirstBackup(t *testing.T) {
	t.Parallel()
	ran := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	old := addonCR(t, "old-pg", "old", kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"})
	fresh := addonCR(t, "new-pg", "new", kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"})
	old.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-48 * time.Hour)))
	fresh.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-time.Hour)))
	kc := instanceKube(t, []*unstructured.Unstructured{
		addonCR(t, "kuso-instance-pg", "kuso-instance", kube.KusoAddonSpec{Kind: "postgres",
			Backup: &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 14}}),
		old, fresh,
	},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: "kuso"}},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-pg-backup", Namespace: "kuso"},
			Status:     batchv1.CronJobStatus{LastScheduleTime: &ran, LastSuccessfulTime: &ran},
		},
	)
	rows, _ := ComputeAddons(context.Background(), kc, "kuso")
	if r := rowFor(t, rows, "old-pg"); r.Unprotected || r.LastSuccessAt == "" {
		t.Errorf("tenant older than the last run is covered by it: %+v", r)
	}
	r := rowFor(t, rows, "new-pg")
	if !r.Unprotected || r.LastSuccessAt != "" || !strings.Contains(r.Detail, "pending first backup") {
		t.Errorf("tenant newer than the last run must read pending, not ok: %+v", r)
	}
}

// An unscheduled native database is unprotected data, not a neutral gap;
// an unscheduled cache, external addon or env clone is not.
func TestComputeAddons_UnscheduledDatabaseUnprotected(t *testing.T) {
	t.Parallel()
	clone := addonCR(t, "acme-pg-pr-3", "acme", kube.KusoAddonSpec{Kind: "postgres"})
	clone.SetLabels(map[string]string{"kuso.sislelabs.com/project": "acme", kube.LabelEnv: "preview-pr-3"})
	kc := instanceKube(t, []*unstructured.Unstructured{
		addonCR(t, "acme-pg", "acme", kube.KusoAddonSpec{Kind: "postgres"}),
		addonCR(t, "acme-my", "acme", kube.KusoAddonSpec{Kind: "mysql"}),
		addonCR(t, "acme-cache", "acme", kube.KusoAddonSpec{Kind: "redis"}),
		addonCR(t, "acme-ext", "acme", kube.KusoAddonSpec{Kind: "postgres", External: &kube.KusoAddonExternal{SecretName: "x"}}),
		clone,
	})
	rows, _ := ComputeAddons(context.Background(), kc, "kuso")
	for name, want := range map[string]bool{"acme-pg": true, "acme-my": true, "acme-cache": false, "acme-ext": false, "acme-pg-pr-3": false} {
		r := rowFor(t, rows, name)
		if r.Unprotected != want || !r.Healthy {
			t.Errorf("%s: unprotected=%v healthy=%v, want unprotected=%v healthy=true", name, r.Unprotected, r.Healthy, want)
		}
	}
}
