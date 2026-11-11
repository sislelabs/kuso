package nodewatch

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

func upNode(name string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
}

func activatorPod(name, node string, ready bool, created time.Time) corev1.Pod {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: activatorNamespace, CreationTimestamp: metav1.NewTime(created),
			Labels: map[string]string{"app.kubernetes.io/name": "kuso-activator"}},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}},
	}
}

// RUN-6: both activator replicas on one node with a free second node →
// evict the newer one; every other shape leaves them alone.
func TestColocatedVictim(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	cordoned := upNode("b")
	cordoned.Spec.Unschedulable = true
	tainted := upNode("b")
	tainted.Spec.Taints = []corev1.Taint{{Key: "x", Effect: corev1.TaintEffectNoSchedule}}
	both := func(readyB bool) []corev1.Pod {
		return []corev1.Pod{activatorPod("old", "a", true, t0), activatorPod("new", "a", readyB, t0.Add(time.Minute))}
	}
	ab := []*corev1.Node{upNode("a"), upNode("b")}
	cases := []struct {
		name  string
		pods  []corev1.Pod
		nodes []*corev1.Node
		want  string
	}{
		{"co-located, free node", both(true), ab, "new"},
		{"already spread", []corev1.Pod{activatorPod("p1", "a", true, t0), activatorPod("p2", "b", true, t0)}, ab, ""},
		{"single node", both(true), []*corev1.Node{upNode("a")}, ""},
		{"other node cordoned", both(true), []*corev1.Node{upNode("a"), cordoned}, ""},
		{"other node tainted", both(true), []*corev1.Node{upNode("a"), tainted}, ""},
		{"replica not ready", both(false), ab, ""},
		{"one replica", []corev1.Pod{activatorPod("p1", "a", true, t0)}, ab, ""},
	}
	for _, c := range cases {
		got := colocatedVictim(c.pods, c.nodes)
		name := ""
		if got != nil {
			name = got.Name
		}
		if name != c.want {
			t.Errorf("%s: victim %q, want %q", c.name, name, c.want)
		}
	}
}

func TestRebalanceActivatorEvictsOncePerWindow(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	p1, p2 := activatorPod("p1", "a", true, t0), activatorPod("p2", "a", true, t0.Add(time.Second))
	cs := kubefake.NewSimpleClientset(&p1, &p2)
	evictions := 0
	cs.PrependReactor("create", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetSubresource() == "eviction" {
			evictions++
			return true, nil, nil
		}
		return false, nil, nil
	})
	w := &Watcher{Kube: &kube.Client{Clientset: cs}, Logger: slogDiscard()}
	nodes := []*corev1.Node{upNode("a"), upNode("b")}
	w.rebalanceActivator(context.Background(), nodes, t0)
	w.rebalanceActivator(context.Background(), nodes, t0.Add(time.Minute))
	if evictions != 1 {
		t.Fatalf("evictions within one window = %d, want 1", evictions)
	}
	w.rebalanceActivator(context.Background(), nodes, t0.Add(activatorRebalanceEvery+time.Minute))
	if evictions != 2 {
		t.Fatalf("evictions after the window = %d, want 2", evictions)
	}
}
