package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// The key is minted once and never replaced: CP dumps already in the bucket
// are sealed with it, and export must find it by the backup-key label.
func TestEnsureControlPlaneBackupKey(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	created, err := EnsureControlPlaneBackupKey(ctx, cs, "kuso")
	if err != nil || !created {
		t.Fatalf("first ensure: created=%v err=%v", created, err)
	}
	first, _ := ControlPlaneBackupKey(ctx, cs, "kuso")
	if len(first) < 32 {
		t.Fatalf("key too short: %q", first)
	}
	sec, _ := cs.CoreV1().Secrets("kuso").Get(ctx, ControlPlaneBackupKeySecret, metav1.GetOptions{})
	if sec.Labels["kuso.sislelabs.com/role"] != "backup-key" {
		t.Errorf("key Secret lacks the backup-key role label, so export would skip it: %v", sec.Labels)
	}
	if created, err := EnsureControlPlaneBackupKey(ctx, cs, "kuso"); err != nil || created {
		t.Fatalf("second ensure: created=%v err=%v", created, err)
	}
	if again, _ := ControlPlaneBackupKey(ctx, cs, "kuso"); again != first {
		t.Error("ensure replaced an existing key")
	}

	imported := fake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: ControlPlaneBackupKeySecret, Namespace: "kuso"},
		Data:       map[string][]byte{"key": []byte("from-the-old-cluster")},
	})
	if _, err := EnsureControlPlaneBackupKey(ctx, imported, "kuso"); err != nil {
		t.Fatal(err)
	}
	if k, _ := ControlPlaneBackupKey(ctx, imported, "kuso"); k != "from-the-old-cluster" {
		t.Errorf("imported key overwritten: %q", k)
	}
}
