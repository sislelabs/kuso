package handlers

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// BLOB-R1: a scoped project restores through its own key; the bucket-wide
// key is never copied into its namespace. Unscoped projects keep the
// mirrored shared Secret.
func TestRestoreCredentialSecret_PrefersScopedKey(t *testing.T) {
	ctx := context.Background()
	shared := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: "kuso"},
		Data: map[string][]byte{"accessKeyId": []byte("SHARED")}}
	scoped := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-backup-s3-shop", Namespace: "kuso-shop"},
		Data: map[string][]byte{"accessKeyId": []byte("SHOP")}}
	cs := k8sfake.NewSimpleClientset(shared, scoped)
	h := &BackupsHandler{Kube: &kube.Client{Clientset: cs}, Namespace: "kuso"}

	got, err := h.restoreCredentialSecret(ctx, "kuso-shop", "shop")
	if err != nil || got != "kuso-backup-s3-shop" {
		t.Fatalf("scoped project: got %q, %v", got, err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-shop").Get(ctx, backupSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("bucket-wide key was copied into a scoped project's namespace")
	}
	for _, e := range restoreS3EnvFrom(got, "") {
		if e.ValueFrom != nil && e.ValueFrom.SecretKeyRef.Name != "kuso-backup-s3-shop" {
			t.Errorf("%s reads %s", e.Name, e.ValueFrom.SecretKeyRef.Name)
		}
	}

	got, err = h.restoreCredentialSecret(ctx, "kuso-blog", "blog")
	if err != nil || got != backupSecretName {
		t.Fatalf("unscoped project: got %q, %v", got, err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-blog").Get(ctx, backupSecretName, metav1.GetOptions{}); err != nil {
		t.Fatalf("unscoped project did not get the shared mirror: %v", err)
	}
}
