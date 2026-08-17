package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// GetPod backs the log-stream phase watcher, which probes one pod's
// status every 2s for as long as a viewer holds the log panel open.
// Served live that was one apiserver GET per pod per viewer per tick;
// served from the Pod informer (already running cluster-wide for
// ListPodsByLabel) it costs nothing extra.
func TestGetPod_ServesFromInformer(t *testing.T) {
	cs := fake.NewSimpleClientset(
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "kuso"},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		},
	)
	c := &Client{Clientset: cs, Dynamic: fakeClient(t).Dynamic}
	c.Cache = NewCache(c)
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	pod, ok := c.Cache.GetPod("kuso", "web-abc")
	if !ok {
		t.Fatal("GetPod: pod not served from cache")
	}
	if pod.Status.Phase != corev1.PodRunning {
		t.Errorf("phase = %q, want Running", pod.Status.Phase)
	}
}

// A miss must report (nil, false) rather than an empty pod. The phase
// watcher distinguishes "not in cache" from "pod says nothing" and falls
// back to a live Get on the former — an empty-but-true result would make
// it emit a bogus phase and never fall back.
func TestGetPod_MissReportsNotOK(t *testing.T) {
	cs := fake.NewSimpleClientset()
	c := &Client{Clientset: cs, Dynamic: fakeClient(t).Dynamic}
	c.Cache = NewCache(c)
	c.Cache.Start()
	t.Cleanup(c.Cache.Stop)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !c.Cache.WaitForSync(ctx) {
		t.Fatal("informers did not sync")
	}

	if pod, ok := c.Cache.GetPod("kuso", "does-not-exist"); ok || pod != nil {
		t.Errorf("GetPod(missing) = (%v, %v), want (nil, false)", pod, ok)
	}
	// Wrong namespace must not cross-match.
	if _, ok := c.Cache.GetPod("other-ns", "web-abc"); ok {
		t.Error("GetPod matched across namespaces")
	}
}

// A nil Cache / unsynced informer must be safe to call. The phase
// watcher calls this on every tick and treats false as "use the live
// API", so a panic here would take down every log stream.
func TestGetPod_NilCacheIsSafe(t *testing.T) {
	var c *Cache
	if pod, ok := c.GetPod("kuso", "web-abc"); ok || pod != nil {
		t.Errorf("nil Cache GetPod = (%v, %v), want (nil, false)", pod, ok)
	}

	// Constructed but never Start()ed: podSynced returns false, so the
	// lister must not be consulted.
	unstarted := &Cache{}
	if _, ok := unstarted.GetPod("kuso", "web-abc"); ok {
		t.Error("unsynced Cache reported a hit")
	}
}
