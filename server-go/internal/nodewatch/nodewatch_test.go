package nodewatch

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func slogDiscard() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func readyNode(name string, unschedulable bool, ann map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
		}},
	}
}

func notReadyNode(name string, unschedulable bool, ann map[string]string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: ann},
		Spec:       corev1.NodeSpec{Unschedulable: unschedulable},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{
			{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
		}},
	}
}

// TestTickUncordonsOnMarkerAcrossRestart covers the P2 bug: auto-uncordon
// keyed off the in-memory `alerted` map is lost across a server/leader
// restart. A node that went NotReady + got cordoned by us BEFORE the
// restart, then recovered to Ready WHILE we were down, comes back with an
// empty `alerted` map. Recovery must still uncordon it because the node
// carries OUR durable cordon marker.
func TestTickUncordonsOnMarkerAcrossRestart(t *testing.T) {
	t.Parallel()
	node := readyNode("worker", true, map[string]string{
		CordonAnnotation: "true", // we cordoned it before the restart
	})
	cs := kubefake.NewSimpleClientset(node)
	// Fresh Watcher: empty alerted map, as after a restart.
	w := &Watcher{
		Kube:   &kube.Client{Clientset: cs},
		Logger: slogDiscard(),
	}
	w.notReadySince = map[string]time.Time{}
	w.alerted = map[string]struct{}{}

	w.tick(context.Background())

	got, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if got.Spec.Unschedulable {
		t.Error("node carrying our marker + Ready must be uncordoned even with empty alerted map (restart case)")
	}
	if got.Annotations[CordonAnnotation] != "" {
		t.Errorf("cordon marker should be cleared after uncordon, got %q", got.Annotations[CordonAnnotation])
	}
}

// TestTickDoesNotUncordonForeignCordon verifies that a Ready node WITHOUT
// our marker (e.g. an operator's manual cordon) is left untouched — we
// only uncordon nodes we own.
func TestTickDoesNotUncordonForeignCordon(t *testing.T) {
	t.Parallel()
	node := readyNode("worker", true, nil) // cordoned, but not by us
	cs := kubefake.NewSimpleClientset(node)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard()}
	w.notReadySince = map[string]time.Time{}
	w.alerted = map[string]struct{}{}

	w.tick(context.Background())

	got, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !got.Spec.Unschedulable {
		t.Error("a foreign cordon (no marker) must NOT be uncordoned by nodewatch")
	}
}

// TestTickRetriesUncordonAfterFailure covers the "transient uncordon
// failure never retries" half of the P2 bug. Because recovery keys off the
// persisted marker (not the in-memory map), a failed uncordon leaves the
// marker in place, and the NEXT tick re-observes it on the Ready node and
// re-enqueues the uncordon. We simulate the retry by ticking twice with a
// reactor that fails the first patch and succeeds the second.
func TestTickRetriesUncordonAfterFailure(t *testing.T) {
	t.Parallel()
	node := readyNode("worker", true, map[string]string{CordonAnnotation: "true"})
	cs := kubefake.NewSimpleClientset(node)

	failNext := true
	cs.PrependReactor("patch", "nodes", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if failNext {
			failNext = false
			return true, nil, fmt.Errorf("simulated transient apiserver error")
		}
		return false, nil, nil // fall through to default (real patch)
	})

	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard()}
	w.notReadySince = map[string]time.Time{}
	w.alerted = map[string]struct{}{}

	// Tick 1: uncordon patch fails; marker + cordon must remain.
	w.tick(context.Background())
	got, _ := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if !got.Spec.Unschedulable || got.Annotations[CordonAnnotation] != "true" {
		t.Fatalf("tick 1: failed uncordon must leave node cordoned+marked, got unschedulable=%v marker=%q",
			got.Spec.Unschedulable, got.Annotations[CordonAnnotation])
	}

	// Tick 2: re-observes marker on Ready node, retries, succeeds.
	w.tick(context.Background())
	got, _ = cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if got.Spec.Unschedulable {
		t.Error("tick 2: uncordon should have been retried and succeeded")
	}
	if got.Annotations[CordonAnnotation] != "" {
		t.Errorf("tick 2: marker should be cleared after successful retry, got %q", got.Annotations[CordonAnnotation])
	}
}

// TestCordonSkipsAlreadyUnschedulable covers the P2 bug where cordon()
// stamped OUR ownership marker onto a node that was ALREADY unschedulable
// (cordoned by an operator/pkgupdates/manual kubectl). That false claim
// made recovery later uncordon a node kuso never cordoned. cordon() must
// leave an already-unschedulable node completely alone.
func TestCordonSkipsAlreadyUnschedulable(t *testing.T) {
	t.Parallel()
	// Node the operator already cordoned for maintenance — no marker.
	node := notReadyNode("worker", true, nil)
	cs := kubefake.NewSimpleClientset(node)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard()}

	if err := w.cordon(context.Background(), node); err != nil {
		t.Fatalf("cordon: %v", err)
	}

	got, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if _, has := got.Annotations[CordonAnnotation]; has {
		t.Error("cordon() must NOT stamp ownership marker on an already-unschedulable node")
	}
}

// TestCordonMarksNodeWeTransition confirms the happy path still works:
// cordon() DOES cordon + stamp the marker on a schedulable node we
// transition to unschedulable.
func TestCordonMarksNodeWeTransition(t *testing.T) {
	t.Parallel()
	node := notReadyNode("worker", false, nil) // schedulable, gone NotReady
	cs := kubefake.NewSimpleClientset(node)
	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard()}

	if err := w.cordon(context.Background(), node); err != nil {
		t.Fatalf("cordon: %v", err)
	}

	got, err := cs.CoreV1().Nodes().Get(context.Background(), "worker", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if !got.Spec.Unschedulable {
		t.Error("cordon() should have made the node unschedulable")
	}
	if got.Annotations[CordonAnnotation] != "true" {
		t.Errorf("cordon() should stamp our marker on a node we transition, got %q", got.Annotations[CordonAnnotation])
	}
}
