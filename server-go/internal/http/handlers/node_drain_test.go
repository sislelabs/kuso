package handlers

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func nodePod(ns, name, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       corev1.PodSpec{NodeName: node},
	}
}

// evictionReactor answers pods/eviction: blocked pods get a PDB 429,
// everything else is deleted from the tracker like the apiserver would.
func evictionReactor(cs *kubefake.Clientset, blocked map[string]bool, evicted *[]string) {
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		ca := a.(k8stesting.CreateAction)
		name := ca.GetObject().(metav1.Object).GetName()
		if blocked[name] {
			return true, nil, apierrors.NewTooManyRequests("Cannot evict pod as it would violate the pod's disruption budget.", 0)
		}
		*evicted = append(*evicted, name)
		_ = cs.Tracker().Delete(corev1.SchemeGroupVersion.WithResource("pods"), a.GetNamespace(), name)
		return true, nil, nil
	})
}

func shortDrainBudgets(t *testing.T) {
	prevRetry, prevWait := evictRetryFor, drainGoneWait
	evictRetryFor, drainGoneWait = 200*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { evictRetryFor, drainGoneWait = prevRetry, prevWait })
}

// LOOP-14: node remove must go through the Eviction API (PDBs), not
// delete pods directly.
func TestDrainNode_UsesEvictionAPI(t *testing.T) {
	shortDrainBudgets(t)
	cs := kubefake.NewSimpleClientset(nodePod("shop", "web-1", "worker-1"))
	var evicted []string
	evictionReactor(cs, nil, &evicted)
	deletes := 0
	cs.PrependReactor("delete", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		deletes++
		return false, nil, nil
	})
	if err := drainNode(context.Background(), &kube.Client{Clientset: cs}, "worker-1", false); err != nil {
		t.Fatalf("drainNode: %v", err)
	}
	if len(evicted) != 1 || deletes != 0 {
		t.Errorf("evicted=%v direct deletes=%d; want one eviction and no direct delete", evicted, deletes)
	}
}

// A PDB that never allows the eviction must stop the remove with a clear
// error after a bounded wait, leaving the pod in place.
func TestDrainNode_PDBBlockedFailsClearly(t *testing.T) {
	shortDrainBudgets(t)
	cs := kubefake.NewSimpleClientset(nodePod("shop", "db-0", "worker-1"))
	var evicted []string
	evictionReactor(cs, map[string]bool{"db-0": true}, &evicted)
	err := drainNode(context.Background(), &kube.Client{Clientset: cs}, "worker-1", false)
	if err == nil || !strings.Contains(err.Error(), "disruption budget") {
		t.Fatalf("err = %v, want a disruption-budget error", err)
	}
	if _, gerr := cs.CoreV1().Pods("shop").Get(context.Background(), "db-0", metav1.GetOptions{}); gerr != nil {
		t.Error("PDB-protected pod must not be deleted")
	}
}

// Without the pods/eviction RBAC grant (the updater can't add it), fall
// back to the previous graceful delete instead of breaking node remove.
func TestDrainNode_EvictionForbiddenFallsBackToDelete(t *testing.T) {
	shortDrainBudgets(t)
	cs := kubefake.NewSimpleClientset(nodePod("shop", "web-1", "worker-1"))
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() != "eviction" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/eviction"}, "web-1", nil)
	})
	if err := drainNode(context.Background(), &kube.Client{Clientset: cs}, "worker-1", false); err != nil {
		t.Fatalf("drainNode: %v", err)
	}
	if _, err := cs.CoreV1().Pods("shop").Get(context.Background(), "web-1", metav1.GetOptions{}); err == nil {
		t.Error("pod should have been deleted by the fallback")
	}
}

// kuso-server must not drain the node it runs on: it would kill itself
// mid-request and leave the node cordoned but not deleted.
func TestRemoveNode_RefusesOwnNode(t *testing.T) {
	host, err := os.Hostname()
	if err != nil {
		t.Skip("no hostname")
	}
	cs := kubefake.NewSimpleClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}},
		nodePod("kuso", host, "worker-1"),
	)
	rec := removeNodeRequest(t, cs, "")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), "kuso-server") {
		t.Fatalf("status=%d body=%s; want 409 naming kuso-server", rec.Code, rec.Body.String())
	}
	if _, err := cs.CoreV1().Nodes().Get(context.Background(), "worker-1", metav1.GetOptions{}); err != nil {
		t.Error("node must not be deleted")
	}
}
