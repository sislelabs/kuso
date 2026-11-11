package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func retainedAddonObjects(project, fqn string) []runtime.Object {
	lbl := map[string]string{kube.LabelProject: project}
	return []runtime.Object{
		&corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{Name: "data-" + fqn + "-0", Namespace: "kuso",
			Labels: map[string]string{"app.kubernetes.io/name": "kusoaddon", "app.kubernetes.io/instance": fqn}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fqn + "-conn", Namespace: "kuso", Labels: lbl}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: fqn + "-root", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: project, s3RoleLabel: "s3-root"}}},
	}
}

// BLOB-R7: after a plain delete the CR is gone but its data stays, and
// re-adding the name is refused. --purge-data must still reach that data.
func TestDeleteWith_PurgeAfterCRGone(t *testing.T) {
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"))
	cs := kubefake.NewSimpleClientset(retainedAddonObjects("alpha", "alpha-files")...)
	s.Kube.Clientset = cs

	if err := s.DeleteWith(ctx, "alpha", "files", DeleteOptions{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("plain delete of a gone CR: err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteWith(ctx, "alpha", "files", DeleteOptions{PurgeData: true}); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if pvcs, _ := cs.CoreV1().PersistentVolumeClaims("kuso").List(ctx, metav1.ListOptions{}); len(pvcs.Items) != 0 {
		t.Errorf("data PVC survived the purge: %v", pvcs.Items[0].Name)
	}
	for _, n := range []string{"alpha-files-conn", "alpha-files-root"} {
		if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, n, metav1.GetOptions{}); err == nil {
			t.Errorf("%s survived the purge", n)
		}
	}
}

// "foo" + "bar-pg" and "foo-bar" + "pg" name the same fqn; the kept
// Secrets' project label decides, so another project can't purge it.
func TestDeleteWith_PurgeAfterCRGoneRefusesOtherProject(t *testing.T) {
	ctx := context.Background()
	s := fakeService(t, seedProj("foo"), seedProj("foo-bar"))
	cs := kubefake.NewSimpleClientset(retainedAddonObjects("foo-bar", "foo-bar-pg")...)
	s.Kube.Clientset = cs

	if err := s.DeleteWith(ctx, "foo", "bar-pg", DeleteOptions{PurgeData: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := cs.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-foo-bar-pg-0", metav1.GetOptions{}); err != nil {
		t.Error("another project's data was purged")
	}
}
