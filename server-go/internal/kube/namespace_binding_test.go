package kube

import (
	"context"
	"errors"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8sfake "k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

// TestEnsureManagedNSBinding_RetriesTransient is the HIGH-7 robustness
// guard the live smoke test motivated: right after a namespace is
// created, the RoleBinding create can transiently fail (throttling /
// namespace not yet admitting writes). The stamp was one-shot before,
// so a single blip left the namespace permanently without kuso-server
// access. It must now retry and ultimately succeed.
func TestEnsureManagedNSBinding_RetriesTransient(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	var calls int
	cs.PrependReactor("create", "rolebindings", func(a ktesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls < 3 {
			// Transient server error on the first two attempts.
			return true, nil, apierrors.NewServerTimeout(schema.GroupResource{Resource: "rolebindings"}, "create", 1)
		}
		return false, nil, nil // let the default tracker create it
	})

	c := &Client{Clientset: cs}
	if err := c.ensureManagedNSBinding(context.Background(), "kuso-proj"); err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if calls < 3 {
		t.Fatalf("expected at least 3 attempts (2 transient + 1 success), got %d", calls)
	}
	// The binding must actually exist afterwards.
	if _, err := cs.RbacV1().RoleBindings("kuso-proj").Get(context.Background(), managedNSBindingNm, metav1.GetOptions{}); err != nil {
		t.Fatalf("binding not created: %v", err)
	}
}

// TestEnsureManagedNSBinding_AlreadyExistsOK: an existing binding is
// success, not an error (idempotent re-ensure).
func TestEnsureManagedNSBinding_AlreadyExistsOK(t *testing.T) {
	existing := &rbacv1.RoleBinding{}
	existing.Name = managedNSBindingNm
	existing.Namespace = "kuso-proj"
	cs := k8sfake.NewSimpleClientset(existing)
	c := &Client{Clientset: cs}
	if err := c.ensureManagedNSBinding(context.Background(), "kuso-proj"); err != nil {
		t.Fatalf("already-exists should be success, got %v", err)
	}
}

// TestEnsureManagedNSBinding_GivesUp: a persistent error surfaces after
// the retry budget rather than looping forever.
func TestEnsureManagedNSBinding_GivesUp(t *testing.T) {
	cs := k8sfake.NewSimpleClientset()
	cs.PrependReactor("create", "rolebindings", func(a ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("permanent boom")
	})
	c := &Client{Clientset: cs}
	if err := c.ensureManagedNSBinding(context.Background(), "kuso-proj"); err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
}
