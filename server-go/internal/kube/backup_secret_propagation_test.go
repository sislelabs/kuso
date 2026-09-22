package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// Five addons across three custom namespaces reported a successful
// backup every night while writing nothing: the CronJob mounts
// kuso-backup-s3 with optional:true, so a missing Secret leaves BUCKET
// empty and the job exits 0. The Secret only ever existed in `kuso`.
//
// EnsureNamespace is the choke point every project namespace passes
// through, so the copy belongs there.
func TestEnsureBackupSecret_CopiesIntoProjectNamespace(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ServerSANamespace},
		Data:       map[string][]byte{"bucket": []byte("b"), "accessKeyId": []byte("k")},
	})
	c := &Client{Clientset: cs}

	if err := c.ensureBackupSecret(context.Background(), "kuso-koreni"); err != nil {
		t.Fatalf("ensureBackupSecret: %v", err)
	}

	got, err := cs.CoreV1().Secrets("kuso-koreni").Get(context.Background(), backupSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("secret not propagated: %v", err)
	}
	if string(got.Data["bucket"]) != "b" || string(got.Data["accessKeyId"]) != "k" {
		t.Fatalf("payload not copied faithfully: %v", got.Data)
	}
}

// A rotated credential must reach namespaces that already hold a stale
// copy, otherwise those projects keep authenticating with the old key.
func TestEnsureBackupSecret_UpdatesStaleCopy(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ServerSANamespace},
			Data:       map[string][]byte{"secretAccessKey": []byte("new")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: "kuso-koreni"},
			Data:       map[string][]byte{"secretAccessKey": []byte("old")},
		},
	)
	c := &Client{Clientset: cs}

	if err := c.ensureBackupSecret(context.Background(), "kuso-koreni"); err != nil {
		t.Fatalf("ensureBackupSecret: %v", err)
	}

	got, _ := cs.CoreV1().Secrets("kuso-koreni").Get(context.Background(), backupSecretName, metav1.GetOptions{})
	if string(got.Data["secretAccessKey"]) != "new" {
		t.Fatalf("stale copy not refreshed: got %q want %q", got.Data["secretAccessKey"], "new")
	}
}

// Backups genuinely unconfigured is the fresh-install case: there is no
// source Secret, and that must not fail namespace creation.
func TestEnsureBackupSecret_NoSourceIsNotAnError(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	c := &Client{Clientset: cs}

	if err := c.ensureBackupSecret(context.Background(), "kuso-koreni"); err != nil {
		t.Fatalf("missing source must be a benign no-op, got %v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-koreni").Get(context.Background(), backupSecretName, metav1.GetOptions{}); err == nil {
		t.Fatal("must not create an empty Secret when none is configured")
	}
}

// The home namespace is the source; copying onto itself would be a
// self-referential write.
func TestEnsureBackupSecret_SkipsServerSANamespace(t *testing.T) {
	cs := k8sfake.NewSimpleClientset(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ServerSANamespace},
		Data:       map[string][]byte{"bucket": []byte("b")},
	})
	c := &Client{Clientset: cs}
	var writes int
	cs.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		writes++
		return false, nil, nil
	})
	cs.PrependReactor("update", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		writes++
		return false, nil, nil
	})

	if err := c.ensureBackupSecret(context.Background(), ServerSANamespace); err != nil {
		t.Fatalf("ensureBackupSecret: %v", err)
	}
	if writes != 0 {
		t.Fatalf("expected no write to the home namespace, got %d", writes)
	}
}

// The sweep is what fixes namespaces that already exist: EnsureNamespace
// only fires on create/write, so without it an upgrade helps only
// projects created afterwards while the already-broken ones stay broken.
func TestHealBackupSecrets_FillsExistingNamespaces(t *testing.T) {
	mk := func(n string) *corev1.Namespace {
		return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   n,
			Labels: map[string]string{ManagedByLabel: ManagedByValue},
		}}
	}
	cs := k8sfake.NewSimpleClientset(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ServerSANamespace},
			Data:       map[string][]byte{"bucket": []byte("b")},
		},
		mk(ServerSANamespace), mk("kuso-koreni"), mk("kuso-sportnopz"), mk("kuso-nemaserve"),
		// Not kuso-managed: must be left alone.
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
	)
	c := &Client{Clientset: cs}

	n, err := c.HealBackupSecrets(context.Background())
	if err != nil {
		t.Fatalf("HealBackupSecrets: %v", err)
	}
	if n != 3 {
		t.Fatalf("healed %d namespaces, want 3", n)
	}
	for _, ns := range []string{"kuso-koreni", "kuso-sportnopz", "kuso-nemaserve"} {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), backupSecretName, metav1.GetOptions{}); err != nil {
			t.Errorf("%s did not get the backup secret: %v", ns, err)
		}
	}
	if _, err := cs.CoreV1().Secrets("default").Get(context.Background(), backupSecretName, metav1.GetOptions{}); err == nil {
		t.Error("wrote into an unmanaged namespace")
	}
}
