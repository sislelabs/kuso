package activator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

// The last-activity stamp is a GET+UPDATE of the env CR; it must not
// sit in front of proxy.ServeHTTP, or a slow apiserver adds its latency
// to one request per env every activityStampInterval.
func TestRecordActivity_DoesNotBlockOnSlowAPIServer(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	release := make(chan struct{})
	dyn.PrependReactor("get", "kusoenvironments", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return false, nil, nil
	})

	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	kc := &kube.Client{Dynamic: dyn}
	a := New(kc, slog.Default())
	a.nowFn = func() time.Time { return now }

	returned := make(chan struct{})
	go func() {
		a.recordActivity(context.Background(), ns, name)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("recordActivity blocked on the env CR write")
	}
	close(release)

	want := now.Format(time.RFC3339)
	deadline := time.Now().Add(5 * time.Second)
	for {
		e, err := kc.GetKusoEnvironment(context.Background(), ns, name)
		if err == nil && e.Annotations[scaledown.LastActivityAnnotation] == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("stamp never landed asynchronously (err=%v)", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
