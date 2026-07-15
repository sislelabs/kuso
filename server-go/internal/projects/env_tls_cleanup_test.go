package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"

	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// TestDeleteEnvironment_ReclaimsTLSSecrets asserts that deleting a non-production
// env removes its cert-manager TLS Secrets ("<env>-tls" and
// "<env>-tls-extra-<host>"). These carry no ownerReference and aren't in the
// helm release, so without explicit cleanup they leak forever (one per env /
// PR preview).
func TestDeleteEnvironment_ReclaimsTLSSecrets(t *testing.T) {
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

	// Seed the project (namespaceFor needs it) + a staging env CR.
	proj := seedProject("alpha", kube.KusoProjectSpec{DefaultRepo: &kube.KusoRepoRef{URL: "x"}})
	if err := dyn.Tracker().Create(proj.gvr, proj.obj, proj.obj.GetNamespace()); err != nil {
		t.Fatalf("seed project: %v", err)
	}
	env := seedEnv("alpha", "web", "staging", "main", "alpha-web-staging")
	if err := dyn.Tracker().Create(env.gvr, env.obj, env.obj.GetNamespace()); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	// Seed the TLS secrets cert-manager would have created, plus an unrelated
	// secret that must survive.
	tlsPrimary := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging-tls", Namespace: ns}, Type: corev1.SecretTypeTLS}
	tlsExtra := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-staging-tls-extra-staging-example-com", Namespace: ns}, Type: corev1.SecretTypeTLS}
	unrelated := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production-tls", Namespace: ns}, Type: corev1.SecretTypeTLS}
	cs := kubefake.NewSimpleClientset(tlsPrimary, tlsExtra, unrelated)

	s := New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")

	if err := s.DeleteEnvironment(context.Background(), "alpha", "alpha-web-staging"); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}

	mustGone := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("TLS secret %s should have been deleted, err=%v", name, err)
		}
	}
	mustExist := func(name string) {
		if _, err := cs.CoreV1().Secrets(ns).Get(context.Background(), name, metav1.GetOptions{}); err != nil {
			t.Errorf("secret %s should have survived, err=%v", name, err)
		}
	}
	mustGone("alpha-web-staging-tls")
	mustGone("alpha-web-staging-tls-extra-staging-example-com")
	// A different env's TLS secret must NOT be collaterally deleted.
	mustExist("alpha-web-production-tls")
}
