package logs

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

func seedCR(t *testing.T, dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, kind string, obj any) {
	t.Helper()
	m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
		t.Fatal(err)
	}
}

// TestMissingTarget — `kuso logs <missing-project> web` used to say
// "environment not found"; name the level that is actually missing.
func TestMissingTarget(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList",
	})
	seedCR(t, dyn, kube.GVRProjects, "KusoProject", &kube.KusoProject{ObjectMeta: metav1.ObjectMeta{Name: "shop", Namespace: "kuso"}})
	seedCR(t, dyn, kube.GVRServices, "KusoService", &kube.KusoService{ObjectMeta: metav1.ObjectMeta{Name: "shop-web", Namespace: "kuso"}})
	s := &Service{Kube: &kube.Client{Dynamic: dyn}, Namespace: "kuso"}
	ctx := context.Background()
	cases := []struct{ project, service, env, want string }{
		{"nope", "web", "", "project nope not found"},
		{"shop", "api", "", "service shop/api not found"},
		{"shop", "web", "staging", "environment staging not found for service shop/web"},
		{"shop", "web", "", "environment production not found for service shop/web"},
		{"shop", "web", "build:abc", "build:abc not found in project shop"},
	}
	for _, tc := range cases {
		if got := s.MissingTarget(ctx, tc.project, tc.service, tc.env); got != tc.want {
			t.Errorf("%s/%s env=%q: got %q, want %q", tc.project, tc.service, tc.env, got, tc.want)
		}
	}
}
