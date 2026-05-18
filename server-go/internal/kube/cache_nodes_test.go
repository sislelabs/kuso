package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// TestCache_ListNodes_FallsBackWhenNoCache covers the cold path
// callers exercise during the boot window — before Start() has
// kicked the watch, ListNodes returns (nil, false) so the caller
// transparently falls through to a live LIST.
func TestCache_ListNodes_FallsBackWhenNoCache(t *testing.T) {
	t.Parallel()
	c := &Client{} // no Cache, no Clientset
	if nodes, ok := c.Cache.ListNodes(); ok || nodes != nil {
		t.Fatalf("nil cache should report (nil, false), got (%v, %v)", nodes, ok)
	}
}

// TestCache_ListNodes_ReturnsInformerSnapshot is the happy path:
// seed the fake clientset with two nodes, start the informer, wait
// for sync, then call ListNodes and verify both are returned with
// the expected Ready conditions intact (consumers in nodewatch +
// nodemetrics read .Status.Conditions and .Status.Capacity off the
// returned pointers).
func TestCache_ListNodes_ReturnsInformerSnapshot(t *testing.T) {
	t.Parallel()
	clientset := kubefake.NewSimpleClientset(
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionTrue},
				},
				Capacity: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
			},
		},
		&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-b"},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{
					{Type: corev1.NodeReady, Status: corev1.ConditionFalse},
				},
			},
		},
	)
	// NewCache requires a Dynamic client too — kuso's CRD informers
	// run on the dynamic factory. Use the existing fakeClient
	// helper's shape: an empty fake dynamic client is enough since
	// this test only exercises the typed Node informer.
	listKinds := map[schema.GroupVersionResource]string{
		GVRKuso: "KusoList", GVRProjects: "KusoProjectList",
		GVRServices: "KusoServiceList", GVREnvironments: "KusoEnvironmentList",
		GVRAddons: "KusoAddonList", GVRBuilds: "KusoBuildList",
		GVRCrons: "KusoCronList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	c := &Client{Clientset: clientset, Dynamic: dyn}
	c.Cache = NewCache(c)
	if c.Cache == nil {
		t.Fatal("NewCache returned nil")
	}
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)

	// Wait up to 2s for the informer to sync. The fake clientset
	// publishes events immediately, so this is normally microseconds;
	// 2s is a generous CI ceiling.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("Cache.WaitForSync timed out")
	}

	nodes, ok := c.Cache.ListNodes()
	if !ok {
		t.Fatal("ListNodes returned (nil, false) after sync")
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	names := map[string]*corev1.Node{}
	for _, n := range nodes {
		names[n.Name] = n
	}
	if a, ok := names["node-a"]; !ok {
		t.Error("node-a missing from snapshot")
	} else if a.Status.Conditions[0].Status != corev1.ConditionTrue {
		t.Errorf("node-a Ready=%v, want True", a.Status.Conditions[0].Status)
	}
	if b, ok := names["node-b"]; !ok {
		t.Error("node-b missing from snapshot")
	} else if b.Status.Conditions[0].Status != corev1.ConditionFalse {
		t.Errorf("node-b Ready=%v, want False", b.Status.Conditions[0].Status)
	}
}
