package projectsecrets

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// #26: a service subscribed to a key before it existed has no ref for it;
// SetKey must hand the key to OnKeySet so its subscribers get re-propagated
// (ref added, pod rolled) instead of waiting for an unrelated propagation.
func TestSetKey_ResyncsSubscribers(t *testing.T) {
	const ns, project, key = "kuso", "alpha", "LATE_KEY"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			kube.GVREnvironments: "KusoEnvironmentList",
			kube.GVRServices:     "KusoServiceList",
		})
	seedUnstructured(t, dyn, kube.GVRServices, "KusoService", svcWithKeys("web", []string{key}))
	s := New(&kube.Client{Clientset: kubefake.NewSimpleClientset(), Dynamic: dyn}, ns)

	var got string
	s.OnKeySet = func(_ context.Context, p, k string) (int, error) {
		got = p + "/" + k
		return 1, nil
	}
	res, err := s.SetKey(context.Background(), project, key, "v", SetOptions{})
	if err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if got != project+"/"+key {
		t.Fatalf("OnKeySet called with %q, want %s/%s", got, project, key)
	}
	if res.Resynced != 1 {
		t.Errorf("Resynced = %d, want 1", res.Resynced)
	}

	s.OnKeySet = func(context.Context, string, string) (int, error) { return 0, errors.New("boom") }
	if _, err := s.SetKey(context.Background(), project, key, "v2", SetOptions{}); err == nil {
		t.Fatal("a failed resync must surface: the subscribers still lack the key")
	}
}
