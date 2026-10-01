package runs

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"kuso/server/internal/kube"
)

func TestCreate_PlumbsResources(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		kube.GVRRuns:         "KusoRunList",
		kube.GVRServices:     "KusoServiceList",
		kube.GVREnvironments: "KusoEnvironmentList",
		kube.GVRProjects:     "KusoProjectList",
	})
	m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-web-production", Namespace: "kuso"},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-web",
			Image: &kube.KusoImage{Repository: "registry.local/alpha/web", Tag: "sha1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	u := &unstructured.Unstructured{Object: m}
	u.SetGroupVersionKind(kube.GVREnvironments.GroupVersion().WithKind("KusoEnvironment"))
	if err := dyn.Tracker().Create(kube.GVREnvironments, u, "kuso"); err != nil {
		t.Fatal(err)
	}
	s := New(&kube.Client{Dynamic: dyn}, "kuso", slog.Default())

	var req CreateRunRequest
	if err := json.Unmarshal([]byte(`{"command":["migrate"],"resources":{"limits":{"memory":"2Gi"}}}`), &req); err != nil {
		t.Fatal(err)
	}
	run, err := s.Create(context.Background(), "alpha", "web", req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	limits, _ := run.Spec.Resources["limits"].(map[string]any)
	if limits["memory"] != "2Gi" {
		t.Errorf("run resources = %v, want limits.memory=2Gi", run.Spec.Resources)
	}

	time.Sleep(2 * time.Millisecond) // run names are millisecond-stamped
	run, err = s.Create(context.Background(), "alpha", "web", CreateRunRequest{Command: []string{"x"}})
	if err != nil {
		t.Fatal(err)
	}
	if run.Spec.Resources != nil {
		t.Errorf("no resources requested, got %v (want chart default)", run.Spec.Resources)
	}

	if err := json.Unmarshal([]byte(`{"command":["x"],"resources":{"limits":{"memory":"lots"}}}`), &req); err == nil {
		t.Error("invalid quantity decoded without error")
	}
}
