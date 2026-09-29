package runs

import (
	"context"
	"log/slog"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

func TestCreate_CopiesServiceEgress(t *testing.T) {
	cases := []struct{ private, platform bool }{{true, false}, {false, true}, {false, false}}
	for _, tc := range cases {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			kube.GVRRuns:         "KusoRunList",
			kube.GVRServices:     "KusoServiceList",
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRProjects:     "KusoProjectList",
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
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web"},
			Spec:       kube.KusoServiceSpec{Project: "alpha", PrivateEgress: tc.private, PlatformAPIEgress: tc.platform},
		})
		// Env mirror left at zero value: the run must read the service.
		seed(kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production"},
			Spec: kube.KusoEnvironmentSpec{
				Project: "alpha", Service: "alpha-web",
				Image: &kube.KusoImage{Repository: "registry.local/alpha/web", Tag: "sha1"},
			},
		})
		s := New(&kube.Client{Dynamic: dyn}, "kuso", slog.Default())
		run, err := s.Create(context.Background(), "alpha", "web", CreateRunRequest{Command: []string{"echo"}})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if run.Spec.PrivateEgress != tc.private || run.Spec.PlatformAPIEgress != tc.platform {
			t.Errorf("service egress private=%v platform=%v, run got private=%v platform=%v",
				tc.private, tc.platform, run.Spec.PrivateEgress, run.Spec.PlatformAPIEgress)
		}
	}
}
