package backuphealth

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/backup"
	"kuso/server/internal/kube"
)

func backupKey(ns, name string, age time.Duration, exported bool) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			Labels:            map[string]string{backup.KeyRoleLabel: backup.KeyRoleValue},
			CreationTimestamp: metav1.NewTime(time.Now().Add(-age)),
		},
		Data: map[string][]byte{backup.KeySecretKey: []byte("k")},
	}
	if exported {
		s.Annotations = map[string]string{backup.KeyExportedAtAnnotation: "2026-11-01T00:00:00Z"}
	}
	return s
}

// DB-1: an unexported key used to be informational only, so a cluster
// whose every key (control-plane included) existed nowhere else read
// healthy. Past the grace it must fail.
func TestComputeBackupKeys(t *testing.T) {
	t.Parallel()
	day := 48 * time.Hour
	kc := &kube.Client{Clientset: kubefake.NewSimpleClientset(
		backupKey("kuso-p", "p-db-backup-key", day, false),
		backupKey("kuso-p", "p-new-backup-key", time.Hour, false),
		backupKey("kuso-p", "p-done-backup-key", day, true),
		backupKey("kuso", kube.ControlPlaneBackupKeySecret, day, false),
	)}
	ctx := context.Background()

	st := ComputeBackupKeys(ctx, kc, time.Now(), true)
	if st.Healthy || !st.Complete {
		t.Fatalf("old unexported keys must be unhealthy: %+v", st)
	}
	if want := []string{"kuso-p/p-db-backup-key", "kuso/" + kube.ControlPlaneBackupKeySecret}; strings.Join(st.Overdue, ",") != strings.Join(want, ",") {
		t.Errorf("Overdue = %v, want %v", st.Overdue, want)
	}
	if len(st.Unexported) != 3 {
		t.Errorf("Unexported = %v, want the fresh key listed too", st.Unexported)
	}
	if !st.ControlPlaneOverdue || !strings.Contains(st.Detail, "kuso backup keys export") || !strings.Contains(st.Detail, kube.ControlPlaneBackupKeySecret) {
		t.Errorf("detail must name the control-plane key and the export command: %+v", st)
	}

	// Without a control-plane backup the CP key seals nothing.
	st = ComputeBackupKeys(ctx, kc, time.Now(), false)
	if st.ControlPlaneOverdue || len(st.Overdue) != 1 {
		t.Errorf("unconfigured CP backup still counted its key: %+v", st)
	}

	healthy := &kube.Client{Clientset: kubefake.NewSimpleClientset(
		backupKey("kuso-p", "p-done-backup-key", day, true),
		backupKey("kuso-p", "p-new-backup-key", time.Hour, false),
	)}
	if st := ComputeBackupKeys(ctx, healthy, time.Now(), true); !st.Healthy {
		t.Errorf("exported + in-grace keys must be healthy: %+v", st)
	}
}

// The watcher must alert on overdue keys (error when the control-plane key
// is one of them) and clear once they are exported.
func TestTick_UnexportedKeysAlert(t *testing.T) {
	ctx := context.Background()
	cpKey := backupKey("kuso", kube.ControlPlaneBackupKeySecret, 48*time.Hour, false)
	cs := kubefake.NewSimpleClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: stateCarrier, Namespace: "kuso"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: secretName, Namespace: "kuso"}},
		cpKey,
	)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRProjects: "KusoProjectList", kube.GVRAddons: "KusoAddonList"})
	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	w := newProcess(kc)
	w.tick(ctx)
	got := persisted(t, cs)
	if !strings.Contains(got, "backup-keys(kuso/"+kube.ControlPlaneBackupKeySecret+")") || !strings.HasSuffix(got, "|error") {
		t.Fatalf("state = %q, want a backup-keys part at error severity", got)
	}

	cpKey.Annotations = map[string]string{backup.KeyExportedAtAnnotation: "2026-11-15T00:00:00Z"}
	if _, err := cs.CoreV1().Secrets("kuso").Update(ctx, cpKey, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	w.tick(ctx)
	if got := persisted(t, cs); strings.Contains(got, "backup-keys") {
		t.Fatalf("state after export = %q, keys should no longer fail", got)
	}
}
