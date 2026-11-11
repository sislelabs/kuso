package kube

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func scopedFixture(t *testing.T, extra ...*corev1.Secret) (*Client, *k8sfake.Clientset) {
	t.Helper()
	objs := []corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ServerSANamespace}, Data: map[string][]byte{"accessKeyId": []byte("SHARED")}},
		{ObjectMeta: metav1.ObjectMeta{Name: BackupScopedSecretName, Namespace: ServerSANamespace},
			Data: map[string][]byte{"shop.accessKeyId": []byte("AK"), "shop.secretAccessKey": []byte("SK")}},
		{ObjectMeta: metav1.ObjectMeta{Name: ProjectBackupSecretName("shop"), Namespace: "kuso-shop"}, Data: map[string][]byte{"accessKeyId": []byte("AK")}},
	}
	cs := k8sfake.NewSimpleClientset()
	for i := range objs {
		_, _ = cs.CoreV1().Secrets(objs[i].Namespace).Create(context.Background(), &objs[i], metav1.CreateOptions{})
	}
	for _, s := range extra {
		_, _ = cs.CoreV1().Secrets(s.Namespace).Create(context.Background(), s, metav1.CreateOptions{})
	}
	c := fakeClient(t,
		seed(GVRProjects, "KusoProject", ServerSANamespace, "shop", map[string]any{"namespace": "kuso-shop"}),
		seed(GVRProjects, "KusoProject", ServerSANamespace, "blog", map[string]any{"namespace": "kuso-blog"}),
	)
	c.Clientset = cs
	return c, cs
}

func staleSharedCopy(ns string) *corev1.Secret {
	return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: backupSecretName, Namespace: ns,
		Labels: map[string]string{ManagedByLabel: "kuso-server"}}, Data: map[string][]byte{"accessKeyId": []byte("SHARED")}}
}

// BLOB-R1: a namespace whose project has its own scoped key loses the
// bucket-wide copy (left by an old restore or heal); an unscoped one keeps it.
func TestEnsureBackupSecret_ScopedNamespaceDropsSharedKey(t *testing.T) {
	ctx := context.Background()
	c, cs := scopedFixture(t, staleSharedCopy("kuso-shop"))
	if err := c.ensureBackupSecret(ctx, "kuso-shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-shop").Get(ctx, backupSecretName, metav1.GetOptions{}); err == nil {
		t.Error("bucket-wide key left in a fully scoped namespace")
	}
	if err := c.ensureBackupSecret(ctx, "kuso-blog"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-blog").Get(ctx, backupSecretName, metav1.GetOptions{}); err != nil {
		t.Errorf("unscoped namespace lost its backup secret: %v", err)
	}
}

// A CronJob still rendered against the shared Secret keeps it: deleting
// it would turn that CronJob's runs into silent no-op "successes".
func TestEnsureBackupSecret_ScopedNamespaceKeepsReferencedCopy(t *testing.T) {
	ctx := context.Background()
	c, cs := scopedFixture(t, staleSharedCopy("kuso-shop"))
	cj := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "shop-db-backup", Namespace: "kuso-shop"}}
	cj.Spec.JobTemplate.Spec.Template.Spec.Containers = []corev1.Container{{Name: "backup", Env: []corev1.EnvVar{{
		Name: "BUCKET", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: backupSecretName}, Key: "bucket"}}}}}}
	if _, err := cs.BatchV1().CronJobs("kuso-shop").Create(ctx, cj, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ensureBackupSecret(ctx, "kuso-shop"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CoreV1().Secrets("kuso-shop").Get(ctx, backupSecretName, metav1.GetOptions{}); err != nil {
		t.Errorf("shared copy removed while a CronJob still reads it: %v", err)
	}
}

func TestBackupSecretPresent_AcceptsScopedSecret(t *testing.T) {
	ctx := context.Background()
	c, _ := scopedFixture(t)
	if ok, err := c.BackupSecretPresent(ctx, "kuso-shop", "shop"); err != nil || !ok {
		t.Errorf("scoped project: present=%v err=%v", ok, err)
	}
	if ok, err := c.BackupSecretPresent(ctx, "kuso-blog", "blog"); err != nil || ok {
		t.Errorf("project with neither secret: present=%v err=%v", ok, err)
	}
}
