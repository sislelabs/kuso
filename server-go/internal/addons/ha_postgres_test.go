package addons

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

func seedCNPGCluster(name string, labels map[string]string) seed {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(GVRCNPGCluster.GroupVersion().WithKind("Cluster"))
	u.SetName(name)
	u.SetNamespace("kuso")
	u.SetLabels(labels)
	return seed{gvr: GVRCNPGCluster, obj: u}
}

func addonClusterLabels(fqn string) map[string]string {
	return map[string]string{"app.kubernetes.io/name": "kusoaddon", "app.kubernetes.io/instance": fqn}
}

func seedHAAddon(project, short string) seed {
	s := seedPlainAddon(project, short)
	_ = unstructured.SetNestedField(s.obj.Object, true, "spec", "ha")
	return s
}

// DATA-5: the chart keeps the CNPG Cluster and its -app Secret on helm
// uninstall, so purgeData used to leave three Postgres pods holding every
// row, and a re-add adopted them with the old password.
func TestDeleteWith_PurgeDataRemovesHACluster(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), seedHAAddon("alpha", "pg"),
		seedCNPGCluster("alpha-pg", addonClusterLabels("alpha-pg")),
		// The control plane's own CNPG database must never be touched.
		seedCNPGCluster("kuso-postgres", nil))
	cs := kubefake.NewSimpleClientset(connSecret("alpha-pg-conn"),
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg-app", Namespace: "kuso"}})
	s.Kube.Clientset = cs

	if err := s.DeleteWith(ctx, "alpha", "pg", DeleteOptions{PurgeData: true}); err != nil {
		t.Fatalf("DeleteWith: %v", err)
	}
	clusters := s.Kube.Dynamic.Resource(GVRCNPGCluster).Namespace("kuso")
	if _, err := clusters.Get(ctx, "alpha-pg", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("HA cluster survived purge: err=%v", err)
	}
	if _, err := cs.CoreV1().Secrets("kuso").Get(ctx, "alpha-pg-app", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("-app password secret survived purge: err=%v", err)
	}
	if _, err := clusters.Get(ctx, "kuso-postgres", metav1.GetOptions{}); err != nil {
		t.Errorf("unrelated cluster touched: %v", err)
	}
	if _, err := s.Add(ctx, "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); err != nil {
		t.Errorf("re-Add after purge: %v", err)
	}
}

func TestDelete_DefaultKeepsHAClusterAndRefusesReAdd(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s := fakeService(t, seedProj("alpha"), seedHAAddon("alpha", "pg"),
		seedCNPGCluster("alpha-pg", addonClusterLabels("alpha-pg")))
	s.Kube.Clientset = kubefake.NewSimpleClientset()

	if err := s.Delete(ctx, "alpha", "pg"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Kube.Dynamic.Resource(GVRCNPGCluster).Namespace("kuso").Get(ctx, "alpha-pg", metav1.GetOptions{}); err != nil {
		t.Errorf("HA cluster should be retained without purge: %v", err)
	}
	if _, err := s.Add(ctx, "alpha", CreateAddonRequest{Name: "pg", Kind: "postgres"}); !errors.Is(err, ErrConflict) {
		t.Errorf("re-Add over a retained HA cluster: %v, want ErrConflict", err)
	}
}
