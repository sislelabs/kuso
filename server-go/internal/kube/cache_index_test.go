package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
)

// The project index and pod-instance index must return exactly what the
// equivalent selector scan would, namespace included.
func TestCache_IndexedLookups(t *testing.T) {
	t.Parallel()
	pod := func(ns, name, inst string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name,
			Labels: map[string]string{instanceLabel: inst}}}
	}
	cs := kubefake.NewSimpleClientset(
		pod("kuso", "a-1", "p-web-production"),
		pod("kuso", "a-2", "p-web-production"),
		pod("kuso-other", "b-1", "p-web-production"),
		pod("kuso", "c-1", "p-api-production"),
	)
	listKinds := map[schema.GroupVersionResource]string{
		GVRKuso: "KusoList", GVRProjects: "KusoProjectList",
		GVRServices: "KusoServiceList", GVREnvironments: "KusoEnvironmentList",
		GVRAddons: "KusoAddonList", GVRBuilds: "KusoBuildList",
		GVRCrons: "KusoCronList", GVRRuns: "KusoRunList",
	}
	env := func(ns, name, project, envLabel string) runtime.Object {
		u := crd(GVREnvironments, "KusoEnvironment", ns, name, map[string]any{"project": project})
		u.SetLabels(map[string]string{LabelProject: project, LabelEnv: envLabel})
		return u
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds,
		env("kuso", "p-web-production", "p", "production"),
		env("kuso", "p-web-pr-1", "p", "preview-pr-1"),
		env("kuso-q", "q-web-production", "q", "production"),
		env("kuso", "pp-web-production", "pp", "production"),
	)
	c := &Client{Clientset: cs, Dynamic: dyn}
	c.Cache = NewCache(c)
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("sync timed out")
	}

	pods, ok := c.Cache.ListPodsByInstance("kuso", "p-web-production")
	if !ok || len(pods) != 2 {
		t.Fatalf("ListPodsByInstance: ok=%v n=%d, want 2 (other namespace excluded)", ok, len(pods))
	}

	cases := []struct {
		ns   string
		sel  labels.Set
		want int
	}{
		{"kuso", labels.Set{LabelProject: "p"}, 2},
		{"", labels.Set{LabelProject: "p"}, 2},
		{"kuso", labels.Set{LabelProject: "p", LabelEnv: "production"}, 1},
		{"kuso", labels.Set{LabelProject: "q"}, 0},
		{"", labels.Set{LabelProject: "q"}, 1},
	}
	for _, tc := range cases {
		got, ok := c.Cache.ListFromCacheShared(GVREnvironments, tc.ns, labels.SelectorFromSet(tc.sel))
		if !ok || len(got) != tc.want {
			t.Errorf("ns=%q sel=%v: ok=%v n=%d, want %d", tc.ns, tc.sel, ok, len(got), tc.want)
		}
	}
	envs, err := c.ListKusoEnvironmentsByLabels(ctx, "kuso", map[string]string{LabelProject: "p"})
	if err != nil || len(envs) != 2 {
		t.Fatalf("typed list via index: n=%d err=%v", len(envs), err)
	}
}
