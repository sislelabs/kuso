package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"kuso/server/internal/kube"
)

// newMetricsKube returns a Client whose dynamic fake serves PodMetrics,
// plus a counter of how many LIST calls actually reached it.
func newMetricsKube(t *testing.T, pods ...*unstructured.Unstructured) (*kube.Client, *atomic.Int64) {
	t.Helper()
	scheme := runtime.NewScheme()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme,
		map[schema.GroupVersionResource]string{podMetricsGVR: "PodMetricsList"})
	var calls atomic.Int64
	dyn.PrependReactor("list", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		calls.Add(1)
		items := make([]unstructured.Unstructured, 0, len(pods))
		for _, p := range pods {
			items = append(items, *p)
		}
		return true, &unstructured.UnstructuredList{Items: items}, nil
	})
	return &kube.Client{Dynamic: dyn}, &calls
}

func podMetric(name, instance string, cpu, mem string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "metrics.k8s.io/v1beta1",
		"kind":       "PodMetrics",
		"metadata": map[string]any{
			"name":      name,
			"namespace": "kuso",
			"labels":    map[string]any{"app.kubernetes.io/instance": instance},
		},
		"containers": []any{
			map[string]any{"name": "app", "usage": map[string]any{"cpu": cpu, "memory": mem}},
		},
	}}
}

// The whole point of this cache: the projects dashboard renders one
// card per project, each polling its own metrics endpoint. Those used
// to be N separate metrics-server LISTs per cycle, and metrics-server
// is a single replica that degrades long before the apiserver. N
// concurrent callers must collapse to exactly ONE upstream request.
func TestPodMetricsCache_CoalescesConcurrentCallers(t *testing.T) {
	ResetPodMetricsCacheForTesting()
	kc, calls := newMetricsKube(t, podMetric("p1", "alpha-web-production", "100m", "128Mi"))

	const callers = 50
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			if _, ok := listPodMetricsCached(context.Background(), kc, "kuso"); !ok {
				t.Error("expected metrics to be served")
			}
		}()
	}
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("%d upstream LISTs for %d concurrent callers, want exactly 1 — "+
			"the dashboard fan-out is not being coalesced", n, callers)
	}
}

// A warm entry must be reused, and an expired one refetched.
func TestPodMetricsCache_TTL(t *testing.T) {
	ResetPodMetricsCacheForTesting()
	kc, calls := newMetricsKube(t, podMetric("p1", "alpha-web-production", "100m", "128Mi"))

	for i := 0; i < 5; i++ {
		if _, ok := listPodMetricsCached(context.Background(), kc, "kuso"); !ok {
			t.Fatal("expected metrics")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("%d upstream LISTs for 5 sequential callers within the TTL, want 1", n)
	}

	// Force expiry rather than sleeping the real TTL.
	podMetricsCache.mu.Lock()
	e := podMetricsCache.m["kuso"]
	e.fetched = time.Now().Add(-2 * podMetricsTTL)
	podMetricsCache.m["kuso"] = e
	podMetricsCache.mu.Unlock()

	if _, ok := listPodMetricsCached(context.Background(), kc, "kuso"); !ok {
		t.Fatal("expected metrics after expiry")
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("%d upstream LISTs after TTL expiry, want 2 — a stale entry must be refetched", n)
	}
}

// Filtering by env instance must isolate projects from each other:
// serving one shared namespace-wide fetch must NOT let project A's card
// count project B's pods.
func TestPodMetricsCache_FilterByInstance(t *testing.T) {
	ResetPodMetricsCacheForTesting()
	kc, _ := newMetricsKube(t,
		podMetric("a1", "alpha-web-production", "100m", "128Mi"),
		podMetric("b1", "beta-web-production", "250m", "256Mi"),
	)
	items, ok := listPodMetricsCached(context.Background(), kc, "kuso")
	if !ok {
		t.Fatal("expected metrics")
	}
	mine := map[string]struct{}{"alpha-web-production": {}}
	var cpu, mem int64
	pods := 0
	for i := range items {
		if _, in := mine[podMetricsInstance(items[i])]; !in {
			continue
		}
		if sumPodMetricsUsage(items[i], &cpu, &mem) {
			pods++
		}
	}
	if pods != 1 {
		t.Errorf("pods=%d, want 1 — another project's pods leaked into this card", pods)
	}
	if cpu != 100 {
		t.Errorf("cpu=%dm, want 100m", cpu)
	}
	if mem != 128*1024*1024 {
		t.Errorf("mem=%d, want %d", mem, 128*1024*1024)
	}
}

// An upstream failure must not be cached: metrics are best-effort and
// caching an error would turn a blip into a guaranteed TTL-long outage.
func TestPodMetricsCache_DoesNotCacheErrors(t *testing.T) {
	ResetPodMetricsCacheForTesting()
	kc := &kube.Client{} // nil Dynamic → unavailable
	if _, ok := listPodMetricsCached(context.Background(), kc, "kuso"); ok {
		t.Fatal("expected unavailable")
	}
	podMetricsCache.mu.RLock()
	_, cached := podMetricsCache.m["kuso"]
	podMetricsCache.mu.RUnlock()
	if cached {
		t.Error("a failed fetch was cached; a transient outage would be extended to the full TTL")
	}
}
