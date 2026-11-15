package backuphealth

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// cpBackupSecret is a complete kuso-postgres-backup Secret.
func cpBackupSecret(ns string) *corev1.Secret {
	data := map[string][]byte{}
	for _, k := range requiredSecretKeys {
		data[k] = []byte("v")
	}
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: ns}, Data: data}
}

func cpKeySecret(ns string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: kube.ControlPlaneBackupKeySecret, Namespace: ns},
		Data:       map[string][]byte{"key": []byte("k")},
	}
}

// DB-3 / OPS-8: a Secret missing a key, or a missing encryption key, makes
// every run fail without uploading anything. Health used to check only that
// the Secret existed, and the CronJob's last success (from before the
// breakage, or from the old exit-0 gate) kept it green.
func TestCompute_IncompleteControlPlaneConfigIsUnhealthy(t *testing.T) {
	const ns = "kuso"
	recent := metav1.NewTime(time.Now().Add(-time.Hour))
	cron := &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: cronJobName, Namespace: ns},
		Spec:       batchv1.CronJobSpec{Schedule: "0 23 * * *"},
		Status:     batchv1.CronJobStatus{LastSuccessfulTime: &recent},
	}
	noEndpoint := cpBackupSecret(ns)
	delete(noEndpoint.Data, "KUSO_BACKUP_ENDPOINT")

	cases := []struct {
		name string
		objs []runtime.Object
		want string
	}{
		{"missing endpoint", []runtime.Object{noEndpoint, cpKeySecret(ns), cron}, "missing KUSO_BACKUP_ENDPOINT"},
		{"missing encryption key", []runtime.Object{cpBackupSecret(ns), cron}, kube.ControlPlaneBackupKeySecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(context.Background(), &kube.Client{Clientset: kubefake.NewSimpleClientset(tc.objs...)}, ns)
			if got.Healthy || !strings.Contains(got.Detail, tc.want) {
				t.Errorf("healthy=%v detail=%q, want unhealthy naming %q", got.Healthy, got.Detail, tc.want)
			}
			if sev := alertSeverity(got, RegistryGCStatus{Healthy: true, CronJobPresent: true}); sev != "error" {
				t.Errorf("severity = %s, want error: a configured backup that can't run is a regression", sev)
			}
		})
	}

	ok := Compute(context.Background(), &kube.Client{Clientset: kubefake.NewSimpleClientset(cpBackupSecret(ns), cpKeySecret(ns), cron)}, ns)
	if !ok.Healthy {
		t.Errorf("complete config should be healthy: %+v", ok)
	}
}
