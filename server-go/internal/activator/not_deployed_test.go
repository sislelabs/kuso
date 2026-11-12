package activator

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

// RUN-5: a request to an env still waiting for its first build must not
// scale it up (the pod would get image ":latest" → InvalidImageName) and
// must answer a "not deployed yet" 503, not a "failed to start" 502.
func TestServe_EnvWithoutImageIsNotWoken(t *testing.T) {
	t.Parallel()
	const ns, name = "kuso", "app-web-pr-3"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"kind": "preview", "host": "pr-3.example.com", "replicaCount": int64(0)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatal(err)
	}
	zero := int32(0)
	cs := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       appsv1.DeploymentSpec{Replicas: &zero},
	})
	a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
	a.holdTimeout = 5 * time.Second

	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://pr-3.example.com/", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("status = %d Retry-After=%q (%s), want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
	}
	d, err := cs.AppsV1().Deployments(ns).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if *d.Spec.Replicas != 0 {
		t.Errorf("deployment scaled to %d; an env without an image must stay at 0", *d.Spec.Replicas)
	}
}
