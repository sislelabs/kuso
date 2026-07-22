package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// TestDeleteProject_KeepsHelmOwnedConnSecrets guards the project-delete
// label sweep against destroying helm-owned addon conn/tls Secrets. Those
// carry helm.sh/resource-policy: keep because their data (the addon
// password) must keep matching the surviving pgdata PVC on a
// delete+recreate cycle. Sweeping them mints a fresh password that fails to
// auth against the old data. Imperative kuso-server secrets (the
// <project>-shared secret and friends) must STILL be swept — they're not
// helm-owned and orphan in the shared namespace otherwise.
func TestDeleteProject_KeepsHelmOwnedConnSecrets(t *testing.T) {
	const ns = "kuso"
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRKuso:         "KusoList",
		kube.GVRProjects:     "KusoProjectList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRAddons:       "KusoAddonList",
		kube.GVRBuilds:       "KusoBuildList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)

	proj := seedProject("beta", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}})
	if err := dyn.Tracker().Create(proj.gvr, proj.obj, proj.obj.GetNamespace()); err != nil {
		t.Fatalf("seed project: %v", err)
	}

	// Helm-owned addon conn + tls secrets: project-labelled AND carrying
	// the keep policy. These MUST survive the sweep.
	connSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:        "beta-pg-conn",
		Namespace:   ns,
		Labels:      map[string]string{labelProject: "beta"},
		Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
	}}
	tlsSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:        "beta-pg-tls",
		Namespace:   ns,
		Labels:      map[string]string{labelProject: "beta"},
		Annotations: map[string]string{"helm.sh/resource-policy": "keep"},
	}}

	// Imperative kuso-server secret: project-labelled, NOT helm-owned.
	// Must be swept so it doesn't orphan in the shared namespace and
	// re-seed a same-named project later.
	sharedSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "beta-shared",
		Namespace: ns,
		Labels:    map[string]string{labelProject: "beta"},
	}}

	cs := kubefake.NewSimpleClientset(connSecret, tlsSecret, sharedSecret)
	s := New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")

	// Plain Delete (no PurgeData): the safe path that keeps data.
	if err := s.Delete(context.Background(), "beta"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	mustExist := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("helm-owned secret %s should have survived project delete, err=%v", name, err)
		}
	}
	mustGone := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("imperative secret %s should have been swept, err=%v", name, err)
		}
	}
	mustExist("beta-pg-conn")
	mustExist("beta-pg-tls")
	mustGone("beta-shared")
}
