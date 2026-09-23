package activator

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/kube"
	"kuso/server/internal/scaledown"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// scaledown reads the env's last-activity annotation to decide whether a
// Deployment with replicas > 0 is idle. If the wake's scale-up lands
// before that stamp, a scaledown tick in the cold-start window sees a
// running, "idle" env and re-sleeps it mid-hold, and the held request
// 503s. So the stamp must already be on the env when replicas go up.
func TestDoWake_StampsActivityBeforeScalingUp(t *testing.T) {
	const ns, name = "kuso", "app-web-production"
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	stale := now.Add(-2 * time.Hour).Format(time.RFC3339)

	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata": map[string]any{
			"name":      name,
			"namespace": ns,
			"annotations": map[string]any{
				scaledown.LastActivityAnnotation:     stale,
				scaledown.PreSleepReplicasAnnotation: "2",
			},
		},
		"spec": map[string]any{"replicaCount": int64(0)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatalf("seed env: %v", err)
	}

	zero := int32(0)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	})

	kc := &kube.Client{Clientset: cs, Dynamic: dyn}
	stampAtPatch := "<patch never happened>"
	cs.PrependReactor("patch", "deployments", func(k8stesting.Action) (bool, runtime.Object, error) {
		e, err := kc.GetKusoEnvironment(context.Background(), ns, name)
		if err != nil {
			t.Fatalf("read env at patch time: %v", err)
		}
		stampAtPatch = e.Annotations[scaledown.LastActivityAnnotation]
		return false, nil, nil
	})

	a := New(kc, slog.Default())
	a.nowFn = func() time.Time { return now }
	if err := a.doWake(context.Background(), ns, name); err != nil {
		t.Fatalf("doWake: %v", err)
	}

	if want := now.Format(time.RFC3339); stampAtPatch != want {
		t.Fatalf("last-activity when replicas were raised = %q, want %q", stampAtPatch, want)
	}
	dep, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get deployment: %v", err)
	}
	if got := *dep.Spec.Replicas; got != 2 {
		t.Fatalf("replicas after wake = %d, want 2 (pre-sleep count)", got)
	}
}
