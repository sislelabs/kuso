package handlers

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

// env list --env / pods for a base service's env-group must be served by
// the group's clone; every other request passes through untouched.
func TestEnvGroupTarget(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRProjects:     "KusoProjectList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	add := func(gvr schema.GroupVersionResource, kind, ns string, obj any) {
		m, _ := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		if err := dyn.Tracker().Create(gvr, u, ns); err != nil {
			t.Fatal(err)
		}
	}
	add(kube.GVRProjects, "KusoProject", "kuso", &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "puncho", Namespace: "kuso"},
		Spec:       kube.KusoProjectSpec{Namespace: "kuso-puncho"},
	})
	for _, e := range []kube.KusoEnvironment{
		{ObjectMeta: metav1.ObjectMeta{Name: "puncho-web-production", Namespace: "kuso-puncho",
			Labels: map[string]string{kube.LabelProject: "puncho", kube.LabelEnv: "production"}},
			Spec: kube.KusoEnvironmentSpec{Project: "puncho", Service: "puncho-web"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "puncho-web-staging-production", Namespace: "kuso-puncho",
			Labels: map[string]string{kube.LabelProject: "puncho", kube.LabelEnv: "staging"}},
			Spec: kube.KusoEnvironmentSpec{Project: "puncho", Service: "puncho-web-staging"}},
	} {
		add(kube.GVREnvironments, "KusoEnvironment", "kuso-puncho", &e)
	}
	k := &kube.Client{Dynamic: dyn}
	cases := []struct{ service, env, wantSvc, wantEnv string }{
		{"web", "staging", "web-staging", "production"},
		{"web", "production", "web", "production"},
		{"web-staging", "production", "web-staging", "production"},
		{"web", "nope", "web", "nope"},
		{"web", "", "web", ""},
	}
	for _, tc := range cases {
		svc, env := envGroupTarget(context.Background(), k, "kuso", "puncho", tc.service, tc.env)
		if svc != tc.wantSvc || env != tc.wantEnv {
			t.Errorf("(%s, %q) -> (%s, %s), want (%s, %s)", tc.service, tc.env, svc, env, tc.wantSvc, tc.wantEnv)
		}
	}
}
