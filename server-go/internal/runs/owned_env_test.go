package runs

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

// Project "a-b" service "c" owns "a-b-c" and "a-b-c-production". In the
// shared namespace those are also what project "a" service "b-c" derives;
// a run created under "a" must not inherit the victim's image + secrets.
func TestCreate_RefusesAnotherProjectsProductionEnv(t *testing.T) {
	t.Parallel()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRRuns:         "KusoRunList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
	})
	seed := func(gvr schema.GroupVersionResource, kind string, obj any) {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		u := &unstructured.Unstructured{Object: m}
		u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
		u.SetNamespace("kuso")
		if err := dyn.Tracker().Create(gvr, u, "kuso"); err != nil {
			t.Fatalf("seed %s: %v", kind, err)
		}
	}
	seed(kube.GVRServices, "KusoService", &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "a-b-c"},
		Spec:       kube.KusoServiceSpec{Project: "a-b"},
	})
	seed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "a-b-c-production"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "a-b", Service: "a-b-c",
			Image:          &kube.KusoImage{Repository: "registry.local/a-b/c", Tag: "sha1"},
			EnvFromSecrets: []string{"a-b-pg-conn"},
		},
	})
	s := New(&kube.Client{Dynamic: dyn}, "kuso", slog.Default())

	_, err := s.Create(context.Background(), "a", "b-c", CreateRunRequest{Command: []string{"env"}})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Create(a, b-c) = %v, want the not-deployed ErrInvalid", err)
	}
	runs, _ := s.Kube.ListKusoRuns(context.Background(), "kuso")
	if len(runs) != 0 {
		t.Fatalf("a run was created against another project's env: %+v", runs[0].Spec)
	}
}
