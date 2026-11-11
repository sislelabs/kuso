package reconcilehealth

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func s3RoleSecret(name, instance, role string) corev1.Secret {
	return corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso", Labels: map[string]string{
		labelAddonInstance: instance, labelRole: role,
	}}}
}

func TestDetectOrphanS3Secrets(t *testing.T) {
	t.Parallel()
	got := detectOrphanS3Secrets([]corev1.Secret{
		s3RoleSecret("alpha-store-root", "alpha-store", "s3-root"),
		s3RoleSecret("alpha-old-root", "alpha-old", "s3-root"),
		s3RoleSecret("alpha-old-readonly", "alpha-old", "s3-readonly"),
		// Kept by design: the backup key outlives the addon with its dumps.
		s3RoleSecret("alpha-old-backup-key", "alpha-old", "backup-key"),
	}, map[string]bool{"alpha-store": true})
	if len(got) != 2 || got[0].Resource != "alpha-old-root" || got[1].Resource != "alpha-old-readonly" {
		t.Fatalf("got %+v, want alpha-old-root and alpha-old-readonly", got)
	}
	if got[0].Kind != KindOrphanS3Secret || got[0].Safe {
		t.Errorf("issue = %+v; want kind %s, not Safe", got[0], KindOrphanS3Secret)
	}
}

func TestDeleteOrphan_S3SecretRefusesLiveAddon(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	live := s3RoleSecret("alpha-store-root", "alpha-store", "s3-root")
	gone := s3RoleSecret("alpha-old-root", "alpha-old", "s3-root")
	k := orphanTestClient(t, []string{"alpha-store"}, &live, &gone)
	if err := DeleteOrphan(ctx, k, KindOrphanS3Secret, "kuso", "alpha-store-root"); err == nil {
		t.Fatal("deleted the root Secret of a live addon")
	}
	if err := DeleteOrphan(ctx, k, KindOrphanS3Secret, "kuso", "alpha-old-root"); err != nil {
		t.Fatalf("delete orphan: %v", err)
	}
	if _, err := k.Clientset.CoreV1().Secrets("kuso").Get(ctx, "alpha-old-root", metav1.GetOptions{}); err == nil {
		t.Error("orphan survived")
	}
}

func TestS3ConnHoldsRoot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	addon := func(name string) *kube.KusoAddon {
		return &kube.KusoAddon{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"},
			Spec: kube.KusoAddonSpec{Project: "alpha", Kind: "s3"}}
	}
	sec := func(name string, data map[string]string) *corev1.Secret {
		s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kuso"}, Data: map[string][]byte{}}
		for k, v := range data {
			s.Data[k] = []byte(v)
		}
		return s
	}
	cs := kubefake.NewSimpleClientset(
		sec("alpha-legacy-root", map[string]string{"MINIO_ROOT_USER": "ROOTKEY"}),
		sec("alpha-legacy-conn", map[string]string{"S3_ACCESS_KEY_ID": "ROOTKEY"}),
		sec("alpha-scoped-root", map[string]string{"MINIO_ROOT_USER": "ROOTKEY2"}),
		sec("alpha-scoped-conn", map[string]string{"S3_ACCESS_KEY_ID": "APPKEY"}),
	)
	s := &Scanner{Kube: &kube.Client{Clientset: cs}}
	iss, ok := s.s3ConnHoldsRoot(ctx, addon("alpha-legacy"))
	if !ok || iss.Kind != KindS3ConnHoldsRoot {
		t.Fatalf("legacy addon not flagged: %+v %v", iss, ok)
	}
	if iss.RunbookCmd != "kuso project addon s3 rotate-credentials alpha legacy --root" {
		t.Errorf("runbook = %q", iss.RunbookCmd)
	}
	if _, ok := s.s3ConnHoldsRoot(ctx, addon("alpha-scoped")); ok {
		t.Error("scoped addon flagged")
	}
	if _, ok := s.s3ConnHoldsRoot(ctx, addon("alpha-unreconciled")); ok {
		t.Error("addon without a root Secret flagged")
	}
}
