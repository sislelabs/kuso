package scaledown

import (
	"context"
	"testing"
	"time"

	"kuso/server/internal/kube"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func watcherWithEnv(t *testing.T, ns, name string, ann map[string]string) *Watcher {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	annIface := map[string]any{}
	for k, v := range ann {
		annIface[k] = v
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{
			"name":        name,
			"namespace":   ns,
			"annotations": annIface,
		},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, u, ns); err != nil {
		t.Fatalf("seed env: %v", err)
	}
	return &Watcher{Kube: &kube.Client{Dynamic: dyn}, Namespace: ns}
}

// TestRecentlyActive is the HIGH-5a guard: an env whose activator
// last-activity annotation is inside the idle window is treated as active
// (not slept), because the app's own Prometheus counter is always zero
// for activator-routed envs.
func TestRecentlyActive(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name       string
		ann        map[string]string
		idleMin    int
		wantActive bool
	}{
		{"recent activity → active", map[string]string{LastActivityAnnotation: now.Add(-2 * time.Minute).Format(time.RFC3339)}, 30, true},
		{"old activity → idle", map[string]string{LastActivityAnnotation: now.Add(-45 * time.Minute).Format(time.RFC3339)}, 30, false},
		{"no annotation → idle", map[string]string{}, 30, false},
		{"garbage annotation → idle", map[string]string{LastActivityAnnotation: "not-a-time"}, 30, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := watcherWithEnv(t, "kuso", "svc-production", tc.ann)
			w.Now = func() time.Time { return now }
			if got := w.recentlyActive(context.Background(), "kuso", "svc-production", tc.idleMin); got != tc.wantActive {
				t.Fatalf("recentlyActive = %v, want %v", got, tc.wantActive)
			}
		})
	}
}
