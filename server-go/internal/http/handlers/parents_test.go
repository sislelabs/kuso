package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// List endpoints answered 200 [] for a project or service that doesn't
// exist, so a typo in `kuso build list shop wbe` read as "no builds".
func TestRequireParent(t *testing.T) {
	t.Parallel()
	cr := func(gvr schema.GroupVersionResource, kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		u.SetNamespace(ns)
		u.SetName(name)
		_ = unstructured.SetNestedField(u.Object, spec, "spec")
		return u
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects: "KusoProjectList",
		kube.GVRServices: "KusoServiceList",
	})
	for _, s := range []struct {
		gvr schema.GroupVersionResource
		obj *unstructured.Unstructured
	}{
		{kube.GVRProjects, cr(kube.GVRProjects, "KusoProject", "kuso", "shop", map[string]any{})},
		{kube.GVRServices, cr(kube.GVRServices, "KusoService", "kuso", "shop-web", map[string]any{})},
		{kube.GVRProjects, cr(kube.GVRProjects, "KusoProject", "kuso", "koreni", map[string]any{"namespace": "kuso-koreni"})},
		{kube.GVRServices, cr(kube.GVRServices, "KusoService", "kuso-koreni", "koreni-api", map[string]any{})},
	} {
		if err := dyn.Tracker().Create(s.gvr, s.obj, s.obj.GetNamespace()); err != nil {
			t.Fatal(err)
		}
	}
	kc := &kube.Client{Dynamic: dyn}
	for _, tc := range []struct {
		project, service string
		want             int
	}{
		{"shop", "", http.StatusOK},
		{"shop", "web", http.StatusOK},
		{"shop", "shop-web", http.StatusOK},
		{"koreni", "api", http.StatusOK},
		{"nosuch", "", http.StatusNotFound},
		{"shop", "wbe", http.StatusNotFound},
		{"koreni", "web", http.StatusNotFound},
	} {
		rr := httptest.NewRecorder()
		got := http.StatusOK
		if !requireParent(context.Background(), rr, kc, "kuso", tc.project, tc.service) {
			got = rr.Code
		}
		if got != tc.want {
			t.Errorf("%s/%s: got %d, want %d", tc.project, tc.service, got, tc.want)
		}
	}
}
