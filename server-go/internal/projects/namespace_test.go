package projects

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// TestNamespaceFor_FallsBackWhenSpecEmpty proves the legacy single-tenant
// case still works: a project with no spec.namespace resolves to the
// home namespace, not "" or anything weird.
func TestNamespaceFor_FallsBackWhenSpecEmpty(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x"},
		// Namespace deliberately omitted.
	}))
	ns, err := s.namespaceFor(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("namespaceFor: %v", err)
	}
	if ns != s.Namespace {
		t.Errorf("got %q, want home namespace %q", ns, s.Namespace)
	}
}

// TestNamespaceFor_HonorsSpecNamespace proves child resources for a
// project route into spec.namespace when set.
func TestNamespaceFor_HonorsSpecNamespace(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("acme", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x"},
		Namespace:   "acme-prod",
	}))
	ns, err := s.namespaceFor(context.Background(), "acme")
	if err != nil {
		t.Fatalf("namespaceFor: %v", err)
	}
	if ns != "acme-prod" {
		t.Errorf("got %q, want %q", ns, "acme-prod")
	}
}

// TestNamespaceFor_UnknownProject404s prevents the resolver from leaking
// the home namespace for a project that doesn't exist (which would
// otherwise lead a downstream caller to write resources into the home
// ns under a name that conflicts with a real project).
func TestNamespaceFor_UnknownProject404s(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	_, err := s.namespaceFor(context.Background(), "ghost")
	if err == nil {
		t.Fatal("expected error for unknown project, got nil")
	}
}

// TestNamespaceFor_CacheInvalidation proves Update + Delete drop the
// cached entry so the next lookup sees the new state instead of stale.
func TestNamespaceFor_CacheInvalidation(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("beta", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "x"},
		Namespace:   "beta-old",
	}))
	if got, _ := s.namespaceFor(context.Background(), "beta"); got != "beta-old" {
		t.Fatalf("first lookup: %q", got)
	}
	// Mutate the underlying dynamic fixture directly (bypassing
	// s.Update which would invalidate for us).
	gvr := schema.GroupVersionResource{
		Group: "application.kuso.sislelabs.com", Version: "v1alpha1", Resource: "kusoprojects",
	}
	dyn, ok := s.Kube.Dynamic.(*dynfake.FakeDynamicClient)
	if !ok {
		t.Skip("dynamic client is not the fake; can't mutate underlying store")
	}
	got, err := dyn.Resource(gvr).Namespace(s.Namespace).Get(context.Background(), "beta", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if err := unstructured.SetNestedField(got.Object, "beta-new", "spec", "namespace"); err != nil {
		t.Fatal(err)
	}
	if _, err := dyn.Resource(gvr).Namespace(s.Namespace).Update(context.Background(), got, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Cached read still returns the old value.
	if got, _ := s.namespaceFor(context.Background(), "beta"); got != "beta-old" {
		t.Errorf("cached lookup: %q (want beta-old)", got)
	}
	// After invalidation, the next lookup hits the API.
	s.invalidateNamespace("beta")
	if got, _ := s.namespaceFor(context.Background(), "beta"); got != "beta-new" {
		t.Errorf("post-invalidate lookup: %q (want beta-new)", got)
	}
}
