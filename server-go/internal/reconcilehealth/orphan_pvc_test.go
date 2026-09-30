package reconcilehealth

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func addonDataPVC(name, ns, instance string) corev1.PersistentVolumeClaim {
	return corev1.PersistentVolumeClaim{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: ns,
		Labels: map[string]string{
			"app.kubernetes.io/name":     "kusoaddon",
			"app.kubernetes.io/instance": instance,
		},
	}}
}

func TestDetectOrphanAddonPVCs(t *testing.T) {
	t.Parallel()
	terminating := addonDataPVC("data-alpha-old-0", "kuso", "alpha-old")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	pvcs := []corev1.PersistentVolumeClaim{
		addonDataPVC("data-alpha-pg-0", "kuso", "alpha-pg"),
		addonDataPVC("data-alpha-gone-0", "kuso", "alpha-gone"),
		terminating,
		// Service volume, not an addon PVC.
		{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-uploads", Namespace: "kuso",
			Labels: map[string]string{"app.kubernetes.io/name": "kusoenvironment", "app.kubernetes.io/instance": "alpha-web"}}},
	}
	got := detectOrphanAddonPVCs(pvcs, map[string]bool{"alpha-pg": true})
	if len(got) != 1 || got[0].Resource != "data-alpha-gone-0" {
		t.Fatalf("got %+v, want only data-alpha-gone-0", got)
	}
	if got[0].Kind != KindOrphanAddonPVC || got[0].Safe {
		t.Errorf("issue = %+v; want kind orphan_addon_pvc and not Safe", got[0])
	}
}

func orphanTestClient(t *testing.T, addons []string, objs ...runtime.Object) *kube.Client {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRAddons: "KusoAddonList",
	})
	for _, name := range addons {
		u := &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
			"kind":       "KusoAddon",
			"metadata":   map[string]any{"name": name, "namespace": "kuso"},
			"spec":       map[string]any{"kind": "postgres"},
		}}
		if err := dyn.Tracker().Create(kube.GVRAddons, u, "kuso"); err != nil {
			t.Fatalf("seed addon: %v", err)
		}
	}
	return &kube.Client{Dynamic: dyn, Clientset: kubefake.NewSimpleClientset(objs...)}
}

func TestDeleteOrphan_RefusesWhenAddonExists(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pvc := addonDataPVC("data-alpha-pg-0", "kuso", "alpha-pg")
	k := orphanTestClient(t, []string{"alpha-pg"},
		&pvc,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-pg-conn", Namespace: "kuso"}},
	)
	if err := DeleteOrphan(ctx, k, KindOrphanAddonPVC, "kuso", "data-alpha-pg-0"); !errors.Is(err, ErrNotOrphan) {
		t.Errorf("pvc: got %v, want ErrNotOrphan", err)
	}
	if err := DeleteOrphan(ctx, k, KindOrphanConnSecret, "kuso", "alpha-pg-conn"); !errors.Is(err, ErrNotOrphan) {
		t.Errorf("secret: got %v, want ErrNotOrphan", err)
	}
	if _, err := k.Clientset.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-alpha-pg-0", metav1.GetOptions{}); err != nil {
		t.Errorf("live addon's PVC was deleted: %v", err)
	}
}

func TestDeleteOrphan_DeletesWhenAddonGone(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pvc := addonDataPVC("data-alpha-gone-0", "kuso", "alpha-gone")
	k := orphanTestClient(t, nil,
		&pvc,
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-gone-conn", Namespace: "kuso"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "kuso-postgres-conn", Namespace: "kuso"}},
	)
	if err := DeleteOrphan(ctx, k, KindOrphanAddonPVC, "kuso", "data-alpha-gone-0"); err != nil {
		t.Fatalf("pvc: %v", err)
	}
	if err := DeleteOrphan(ctx, k, KindOrphanConnSecret, "kuso", "alpha-gone-conn"); err != nil {
		t.Fatalf("secret: %v", err)
	}
	if _, err := k.Clientset.CoreV1().PersistentVolumeClaims("kuso").Get(ctx, "data-alpha-gone-0", metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("pvc still present: %v", err)
	}
	// Platform credential must never be deletable through this path.
	if err := DeleteOrphan(ctx, k, KindOrphanConnSecret, "kuso", "kuso-postgres-conn"); !errors.Is(err, ErrNotOrphan) {
		t.Errorf("kuso-postgres-conn: got %v, want ErrNotOrphan", err)
	}
}
