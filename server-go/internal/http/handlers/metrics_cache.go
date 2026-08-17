// Package handlers — short-TTL cache for metrics.k8s.io pod metrics.
//
// The problem this solves: the projects dashboard renders one card per
// project and each card polls /api/projects/{p}/metrics every 30s. Each
// request did its own live LIST against metrics.k8s.io. At 100 projects
// that is 100 metrics-server queries per poll cycle per open dashboard,
// and metrics-server is a single replica with no horizontal story — it
// degrades long before the kube apiserver does. It was the first thing
// to fall over as project count grew.
//
// Why a TTL cache and not an informer: metrics.k8s.io is an aggregated
// API backed by an in-memory sliding window. It serves LIST and GET
// only — there is no watch verb — so an informer cannot be built over
// it. A short TTL is the correct tool.
//
// Why namespace-wide and not per-project: every project's pods live in
// the same namespace on the common install shape, and metrics-server
// returns the whole namespace at nearly the same cost as one label
// selector. Fetching once and filtering in memory turns N queries per
// cycle into one, which is the entire point.
//
// Correctness notes:
//
//   - The TTL (5s) is far below the UI's 30s poll and below
//     metrics-server's own ~15s scrape interval, so this never serves
//     data staler than the source already is. Callers see at most one
//     extra scrape-interval of lag, which is invisible on a CPU gauge.
//   - Concurrent callers on a cold entry coalesce through singleflight,
//     so a dashboard opening with 100 cards still issues exactly one
//     upstream request rather than 100 simultaneous misses.
//   - A fetch error is NOT cached. Metrics are a nice-to-have; the
//     handlers already render "—" on failure, and caching an error
//     would extend a transient blip into a guaranteed 5s outage.

package handlers

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
	"golang.org/x/sync/singleflight"
)

// podMetricsGVR is the aggregated-API resource we read.
var podMetricsGVR = schema.GroupVersionResource{
	Group:    "metrics.k8s.io",
	Version:  "v1beta1",
	Resource: "pods",
}

// podMetricsTTL bounds how stale a served entry may be. Deliberately
// shorter than metrics-server's own scrape interval so the cache is
// never the dominant source of lag.
const podMetricsTTL = 5 * time.Second

type podMetricsEntry struct {
	items   []unstructured.Unstructured
	fetched time.Time
}

// podMetricsCache is a process-wide, namespace-keyed TTL cache.
//
// Package-level rather than a handler field because several handlers
// (project cards, env metrics) read the same upstream data; sharing one
// cache means the second of them is free. Safe across replicas — this
// is a read-through cache of a read-only API, so replicas simply each
// hold their own copy.
var podMetricsCache = struct {
	mu sync.RWMutex
	m  map[string]podMetricsEntry
	sf singleflight.Group
}{m: map[string]podMetricsEntry{}}

// listPodMetricsCached returns every PodMetrics object in ns, served
// from cache when fresh. On a miss it fetches once (coalescing
// concurrent callers) and caches the result.
//
// Returns (nil, false) when metrics are unavailable — every caller
// treats that as "render zeros", never as an error to surface.
func listPodMetricsCached(ctx context.Context, kc *kube.Client, ns string) ([]unstructured.Unstructured, bool) {
	if kc == nil || kc.Dynamic == nil {
		return nil, false
	}
	podMetricsCache.mu.RLock()
	e, ok := podMetricsCache.m[ns]
	podMetricsCache.mu.RUnlock()
	if ok && time.Since(e.fetched) < podMetricsTTL {
		return e.items, true
	}

	// Coalesce: a dashboard with N project cards opening at once must
	// produce ONE upstream request, not N.
	v, err, _ := podMetricsCache.sf.Do(ns, func() (any, error) {
		// Re-check under the flight: a caller that queued behind a
		// just-completed fetch should use its result, not issue another.
		podMetricsCache.mu.RLock()
		e, ok := podMetricsCache.m[ns]
		podMetricsCache.mu.RUnlock()
		if ok && time.Since(e.fetched) < podMetricsTTL {
			return e.items, nil
		}
		// No label selector: we fetch the whole namespace once and let
		// callers filter in memory. That is what collapses N per-project
		// queries into one.
		list, lerr := kc.Dynamic.Resource(podMetricsGVR).Namespace(ns).List(ctx, metav1.ListOptions{})
		if lerr != nil {
			return nil, lerr
		}
		podMetricsCache.mu.Lock()
		podMetricsCache.m[ns] = podMetricsEntry{items: list.Items, fetched: time.Now()}
		podMetricsCache.mu.Unlock()
		return list.Items, nil
	})
	if err != nil {
		// Deliberately not cached — see the package comment.
		return nil, false
	}
	items, ok := v.([]unstructured.Unstructured)
	if !ok {
		return nil, false
	}
	return items, true
}

// podMetricsInstance returns the env-instance label of a PodMetrics
// item. metrics-server copies the pod's labels onto the metrics object,
// which is what lets callers filter by env without a second lookup.
func podMetricsInstance(item unstructured.Unstructured) string {
	labels := item.GetLabels()
	if labels == nil {
		return ""
	}
	return labels["app.kubernetes.io/instance"]
}

// sumPodMetricsUsage adds one PodMetrics item's container usage into
// cpuMilli / memBytes. Shared by every caller so the parsing quirks of
// the aggregated API live in exactly one place.
func sumPodMetricsUsage(item unstructured.Unstructured, cpuMilli, memBytes *int64) bool {
	containers, ok := item.Object["containers"].([]any)
	if !ok {
		return false
	}
	for _, c := range containers {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		usage, ok := cm["usage"].(map[string]any)
		if !ok {
			continue
		}
		if cpu, ok := usage["cpu"].(string); ok {
			*cpuMilli += parseCPU(cpu)
		}
		if mem, ok := usage["memory"].(string); ok {
			*memBytes += parseQuantity(mem)
		}
	}
	return true
}

// ResetPodMetricsCacheForTesting drops every cached entry.
func ResetPodMetricsCacheForTesting() {
	podMetricsCache.mu.Lock()
	podMetricsCache.m = map[string]podMetricsEntry{}
	podMetricsCache.mu.Unlock()
}
