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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func addonCR(t *testing.T, name, project string, spec kube.KusoAddonSpec) *unstructured.Unstructured {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoAddon{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{"kuso.sislelabs.com/project": project}},
		Spec:       spec,
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVRAddons.GroupVersion().WithKind("KusoAddon"))
	return u
}

func instanceKube(t *testing.T, addons []*unstructured.Unstructured, objs ...runtime.Object) *kube.Client {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRAddons:   "KusoAddonList",
	})
	for _, a := range addons {
		if err := dyn.Tracker().Create(kube.GVRAddons, a, "kuso"); err != nil {
			t.Fatal(err)
		}
	}
	return &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset(objs...)}
}

func rowFor(t *testing.T, rows []AddonBackupStatus, name string) AddonBackupStatus {
	t.Helper()
	for _, r := range rows {
		if r.Addon == name {
			return r
		}
	}
	t.Fatalf("no row for %s in %+v", name, rows)
	return AddonBackupStatus{}
}

// A tenant DB on the instance PG is only backed up if the instance addon
// itself has a scheduled backup. It used to read Healthy + "via instance"
// even when nothing backed it up anywhere.
func TestComputeAddons_InstanceSharedWithoutBackingBackup(t *testing.T) {
	t.Parallel()
	kc := instanceKube(t, []*unstructured.Unstructured{
		addonCR(t, "kuso-instance-pg", "kuso-instance", kube.KusoAddonSpec{Kind: "postgres"}),
		addonCR(t, "acme-pg", "acme", kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg",
			Backup: &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 14}}),
		addonCR(t, "beta-pg", "beta", kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"}),
	})
	rows, complete := ComputeAddons(context.Background(), kc, "kuso")
	if !complete {
		t.Fatal("incomplete")
	}
	acme := rowFor(t, rows, "acme-pg")
	if acme.Covered || acme.InstanceShared || acme.Healthy || !strings.Contains(acme.Detail, "NOT backed up") {
		t.Errorf("tenant with its own schedule and no backing backup must be uncovered+unhealthy: %+v", acme)
	}
	beta := rowFor(t, rows, "beta-pg")
	if beta.Covered || beta.InstanceShared || !beta.Healthy {
		t.Errorf("unscheduled tenant: uncovered gap, not paged: %+v", beta)
	}
}

func TestComputeAddons_InstanceSharedMirrorsBackingBackup(t *testing.T) {
	t.Parallel()
	now := metav1.NewTime(time.Now().Add(-time.Hour))
	kc := instanceKube(t, []*unstructured.Unstructured{
		addonCR(t, "kuso-instance-pg", "kuso-instance", kube.KusoAddonSpec{Kind: "postgres",
			Backup: &kube.KusoBackup{Schedule: "0 3 * * *", RetentionDays: 14}}),
		addonCR(t, "acme-pg", "acme", kube.KusoAddonSpec{Kind: "postgres", UseInstanceAddon: "pg"}),
	},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3", Namespace: "kuso"}},
		&batchv1.CronJob{
			ObjectMeta: metav1.ObjectMeta{Name: "kuso-instance-pg-backup", Namespace: "kuso"},
			Status:     batchv1.CronJobStatus{LastScheduleTime: &now, LastSuccessfulTime: &now},
		},
	)
	rows, _ := ComputeAddons(context.Background(), kc, "kuso")
	acme := rowFor(t, rows, "acme-pg")
	if !acme.Covered || !acme.InstanceShared || !acme.Healthy || acme.LastSuccessAt == "" {
		t.Errorf("tenant must inherit the instance addon's coverage: %+v", acme)
	}
}
