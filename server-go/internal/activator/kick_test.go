package activator

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// An awake env whose listener flaps refuses dial after dial. Each refused
// dial kicked a wake, and each wake rewrote the env CR, so the helm-operator
// reconciled the release once per proxied request.
func TestKickWake_ThrottledPerEnv(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	if err := dyn.Tracker().Create(kube.GVREnvironments, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"image": map[string]any{"repository": "registry/app", "tag": "v1"}, "replicaCount": int64(1)},
	}}, ns); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	writes := 0
	dyn.PrependReactor("update", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		writes++
		mu.Unlock()
		return false, nil, nil
	})
	one := int32(1)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &one},
	})
	a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
	var now atomic.Int64
	now.Store(time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC).UnixNano())
	a.nowFn = func() time.Time { return time.Unix(0, now.Load()).UTC() }

	settle := func() int {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			a.wakeMu.Lock()
			n := len(a.waking)
			a.wakeMu.Unlock()
			if n == 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		return writes
	}

	// A second apart: the activity stamp has second granularity, and an
	// unchanged stamp skips the write on its own.
	for i := 0; i < 5; i++ {
		a.kickWake(ns, name)
		settle()
		now.Add(int64(time.Second))
	}
	if got := settle(); got != 1 {
		t.Fatalf("env CR writes after 5 refused dials in one window = %d, want 1", got)
	}
	now.Add(int64(kickWakeInterval))
	a.kickWake(ns, name)
	if got := settle(); got != 2 {
		t.Fatalf("env CR writes after the window passed = %d, want 2", got)
	}
}
