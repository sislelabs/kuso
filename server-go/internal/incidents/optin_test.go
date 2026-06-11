package incidents

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// fakeKubeWithProjects builds a *kube.Client backed by dynamic/fake,
// seeded with KusoProject CRs whose incidentMonitoring flag is `monitored`.
func fakeKubeWithProjects(t *testing.T, ns string, monitored map[string]bool) *kube.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	listKinds := map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds)
	for name, on := range monitored {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{
			Group: kube.GVRProjects.Group, Version: kube.GVRProjects.Version, Kind: "KusoProject",
		})
		u.SetNamespace(ns)
		u.SetName(name)
		_ = unstructured.SetNestedField(u.Object, on, "spec", "incidentMonitoring")
		if err := dyn.Tracker().Create(kube.GVRProjects, u, ns); err != nil {
			t.Fatalf("seed project %s: %v", name, err)
		}
	}
	return &kube.Client{Dynamic: dyn}
}

func TestProjectMonitored(t *testing.T) {
	t.Parallel()
	m := &Manager{
		Namespace: "kuso",
		Kube: fakeKubeWithProjects(t, "kuso", map[string]bool{
			"alpha":   true,
			"tickero": false,
		}),
	}

	cases := []struct {
		project string
		want    bool
	}{
		{"alpha", true},      // opted in
		{"tickero", false},   // explicitly off
		{"missing", false},   // unknown project → fail closed
	}
	for _, c := range cases {
		if got := m.projectMonitored(context.Background(), c.project); got != c.want {
			t.Errorf("projectMonitored(%q) = %v, want %v", c.project, got, c.want)
		}
	}
}

// TestProjectMonitored_NilKubeFailsClosed: with no kube client, never
// monitor (don't crash, don't accidentally enroll).
func TestProjectMonitored_NilKubeFailsClosed(t *testing.T) {
	t.Parallel()
	m := &Manager{Namespace: "kuso"}
	if m.projectMonitored(context.Background(), "alpha") {
		t.Error("nil Kube should fail closed (not monitored)")
	}
}
