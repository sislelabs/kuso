package activator

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"kuso/server/internal/kube"
)

func waitingPod(name string, created time.Time, reason string, restarts int32) *corev1.Pod {
	p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: name, Namespace: "kuso", CreationTimestamp: metav1.NewTime(created),
		Labels: map[string]string{"app.kubernetes.io/instance": "app-web-staging"},
	}}
	p.Status.Phase = corev1.PodPending
	st := corev1.ContainerStatus{Name: "web", RestartCount: restarts}
	if reason != "" {
		st.State.Waiting = &corev1.ContainerStateWaiting{Reason: reason}
	}
	p.Status.ContainerStatuses = []corev1.ContainerStatus{st}
	return p
}

func TestNewestPodStartFailure(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		pods []corev1.Pod
		want string
	}{
		{"bad image on the new revision", []corev1.Pod{*waitingPod("old", t0, "", 0), *waitingPod("new", t0.Add(time.Minute), "ImagePullBackOff", 0)}, "ImagePullBackOff"},
		{"only an older pod is broken", []corev1.Pod{*waitingPod("old", t0, "ImagePullBackOff", 0), *waitingPod("new", t0.Add(time.Minute), "ContainerCreating", 0)}, ""},
		{"first crash may be a slow dependency", []corev1.Pod{*waitingPod("p", t0, "CrashLoopBackOff", 1)}, ""},
		{"crash looping", []corev1.Pod{*waitingPod("p", t0, "CrashLoopBackOff", 3)}, "CrashLoopBackOff"},
		{"missing secret", []corev1.Pod{*waitingPod("p", t0, "CreateContainerConfigError", 0)}, "CreateContainerConfigError"},
	}
	for _, c := range cases {
		if got := newestPodStartFailure(c.pods); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// RUN-2: a wake onto a revision that can never start must answer fast with
// the reason, not hold the full budget and then 503 "starting".
func TestServe_FailsFastOnUnstartablePod(t *testing.T) {
	t.Parallel()
	const ns, name = "kuso", "app-web-staging"
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{kube.GVREnvironments: "KusoEnvironmentList"})
	env := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "application.kuso.sislelabs.com/v1alpha1",
		"kind":       "KusoEnvironment",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec":       map[string]any{"kind": "custom", "host": "staging.example.com", "replicaCount": int64(1)},
	}}
	if err := dyn.Tracker().Create(kube.GVREnvironments, env, ns); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewSimpleClientset(waitingPod("web-1", time.Now(), "ImagePullBackOff", 0))
	a := New(&kube.Client{Clientset: cs, Dynamic: dyn}, slog.Default())
	a.holdTimeout = 20 * time.Second

	start := time.Now()
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://staging.example.com/", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d (%s), want 502", rec.Code, rec.Body.String())
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Errorf("took %s; a broken revision must not hold the budget", el)
	}
	var sf *startFailure
	if err := a.waitReady(context.Background(), ns, name); !errors.As(err, &sf) || sf.reason != "ImagePullBackOff" {
		t.Errorf("waitReady err = %v, want startFailure(ImagePullBackOff)", err)
	}
}
