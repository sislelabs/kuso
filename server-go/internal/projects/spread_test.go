package projects

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// node builds a fake corev1.Node with the given Ready + unschedulable
// state for the spread tests.
func node(name string, ready, cordoned bool) *corev1.Node {
	st := corev1.ConditionFalse
	if ready {
		st = corev1.ConditionTrue
	}
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       corev1.NodeSpec{Unschedulable: cordoned},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: st}},
		},
	}
}

// spreadSvc wires a Service with the typed clientset seeded with the
// given nodes (Cache is nil → resolveSpreadPolicy uses the Clientset).
func spreadSvc(t *testing.T, nodes ...*corev1.Node) *Service {
	t.Helper()
	objs := make([]runtime.Object, 0, len(nodes))
	for _, n := range nodes {
		objs = append(objs, n)
	}
	cs := kubefake.NewSimpleClientset(objs...)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(), map[schema.GroupVersionResource]string{})
	return New(&kube.Client{Dynamic: dyn, Clientset: cs}, "kuso")
}

func TestResolveSpreadPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cases := []struct {
		name  string
		nodes []*corev1.Node
		want  string
	}{
		{"two ready nodes → hard", []*corev1.Node{node("a", true, false), node("b", true, false)}, spreadHard},
		{"single node → soft", []*corev1.Node{node("a", true, false)}, spreadSoft},
		{"two nodes but one cordoned → soft", []*corev1.Node{node("a", true, false), node("b", true, true)}, spreadSoft},
		{"two nodes but one NotReady → soft", []*corev1.Node{node("a", true, false), node("b", false, false)}, spreadSoft},
		{"three ready nodes → hard", []*corev1.Node{node("a", true, false), node("b", true, false), node("c", true, false)}, spreadHard},
		{"no nodes → soft (fail safe)", nil, spreadSoft},
	}
	for _, tc := range cases {
		s := spreadSvc(t, tc.nodes...)
		if got := s.resolveSpreadPolicy(ctx); got != tc.want {
			t.Errorf("%s: resolveSpreadPolicy = %q, want %q", tc.name, got, tc.want)
		}
	}

	// nil kube client → soft (never wedge scheduling).
	if got := (&Service{}).resolveSpreadPolicy(ctx); got != spreadSoft {
		t.Errorf("nil kube: got %q, want soft", got)
	}
}
