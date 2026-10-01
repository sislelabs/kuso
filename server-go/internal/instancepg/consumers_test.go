package instancepg

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// A project in its own namespace (kuso-<name>) that uses the cluster DB
// must count as a consumer, or DELETE /api/instance-pg passes its "no
// consumers" gate and drops a DB that project still mounts.
func TestListConsumers_SeesCustomNamespaceProjects(t *testing.T) {
	t.Parallel()
	addon := func(ns, name, project string) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(schema.GroupVersionKind{Group: kube.GVRAddons.Group, Version: kube.GVRAddons.Version, Kind: "KusoAddon"})
		u.SetNamespace(ns)
		u.SetName(name)
		_ = unstructured.SetNestedField(u.Object, map[string]any{"project": project, "kind": "postgres", "useInstanceAddon": "pg"}, "spec")
		return u
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVRAddons: "KusoAddonList"})
	for _, o := range []*unstructured.Unstructured{
		addon("kuso", "home-db", "home"),
		addon("kuso-koreni", "koreni-db", "koreni"),
	} {
		if err := dyn.Tracker().Create(kube.GVRAddons, o, o.GetNamespace()); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
	got, err := s.listConsumers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[p] = true
	}
	if !seen["home"] || !seen["koreni"] || len(got) != 2 {
		t.Fatalf("consumers = %v, want [home koreni]", got)
	}
}
