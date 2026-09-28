package projectsecrets

import (
	"context"
	"reflect"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func seedUnstructured(t *testing.T, dyn *dynamicfake.FakeDynamicClient, gvr schema.GroupVersionResource, kind string, obj any) {
	t.Helper()
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(gvr.GroupVersion().WithKind(kind))
	if _, err := dyn.Resource(gvr).Namespace("kuso").Create(context.Background(), u, metav1.CreateOptions{}); err != nil {
		t.Fatalf("seed %s: %v", kind, err)
	}
}

func svcWithKeys(name string, keys []string) *kube.KusoService {
	return &kube.KusoService{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-" + name, Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: name}},
		Spec: kube.KusoServiceSpec{Project: "alpha", SharedEnvKeys: keys},
	}
}

// F2: `shared-secret set` said "no running envs to roll" while no service
// subscribed to the key (sharedEnvKeys=[] inherits nothing), so the value
// reached no pod and the user got no hint. SetKey must report who
// subscribes, and roll per-key subscribers too: their secretKeyRef is
// resolved at pod start exactly like an envFrom mount.
func TestSetKey_ReportsSubscribersAndRollsPerKeyEnvs(t *testing.T) {
	const ns, project, key = "kuso", "alpha", "SHARED_TOKEN"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRServices:     "KusoServiceList",
		})
	seedUnstructured(t, dyn, kube.GVRServices, "KusoService", svcWithKeys("api", []string{key}))
	seedUnstructured(t, dyn, kube.GVRServices, "KusoService", svcWithKeys("web", []string{}))
	seedUnstructured(t, dyn, kube.GVRServices, "KusoService", svcWithKeys("legacy", nil))
	seedUnstructured(t, dyn, kube.GVRServices, "KusoService", svcWithKeys("other", []string{"OTHER"}))
	seedUnstructured(t, dyn, kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-production", Namespace: ns,
			Labels: map[string]string{kube.LabelProject: project}},
		Spec: kube.KusoEnvironmentSpec{EnvVars: []kube.KusoEnvVar{{
			Name:      key,
			ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": SecretName(project), "key": key}},
		}}},
	})
	seedUnstructured(t, dyn, kube.GVREnvironments, "KusoEnvironment", &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: ns,
			Labels: map[string]string{kube.LabelProject: project}},
	})

	s := New(&kube.Client{Clientset: kubefake.NewSimpleClientset(), Dynamic: dyn}, ns)
	res, err := s.SetKey(context.Background(), project, key, "v", SetOptions{})
	if err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	want := SetResult{Rolled: 1, Subscribers: []string{"api", "legacy"}}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("SetKey = %+v, want %+v", res, want)
	}

	res, err = s.SetKey(context.Background(), project, "NOBODY", "v", SetOptions{})
	if err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if res.Rolled != 0 || !reflect.DeepEqual(res.Subscribers, []string{"legacy"}) {
		t.Fatalf("unsubscribed key: %+v, want only the legacy mount-all service", res)
	}
}
