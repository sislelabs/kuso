package kube

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

// Project purge-delete drops a namespace only when kuso created it. The
// managed-by label can't prove that (EnsureNamespace patches it onto
// adopted namespaces, and users are told to add it themselves), so the
// Create path stamps a separate annotation that the adopt path never adds.
func TestEnsureNamespace_MarksOnlyNamespacesItCreates(t *testing.T) {
	adopted := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name:   "user-ns",
		Labels: map[string]string{ManagedByLabel: ManagedByValue},
	}}
	cs := k8sfake.NewSimpleClientset(adopted)
	c := &Client{Clientset: cs}
	ctx := context.Background()
	for _, ns := range []string{"kuso-fresh", "user-ns"} {
		if err := c.EnsureNamespace(ctx, ns); err != nil {
			t.Fatalf("EnsureNamespace(%s): %v", ns, err)
		}
	}
	fresh, err := cs.CoreV1().Namespaces().Get(ctx, "kuso-fresh", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !NamespaceCreatedByKuso(fresh) {
		t.Errorf("created namespace lacks %s", NamespaceCreatedByKusoAnnotation)
	}
	got, err := cs.CoreV1().Namespaces().Get(ctx, "user-ns", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if NamespaceCreatedByKuso(got) {
		t.Errorf("adopted namespace was marked as kuso-created")
	}
}
