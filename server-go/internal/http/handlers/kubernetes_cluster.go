package handlers

import (
	"net/http"
	"sort"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// clusterCacheTTL bounds how stale the events / storage-class /
// ingress-domains views can be. 30s matches the dashboard's
// refetchInterval; below that we burn kube-apiserver load with no
// user-visible benefit. Above that the cluster-overview tab feels
// laggy when an admin actively edits an Ingress.
const clusterCacheTTL = 30 * time.Second

var clusterCache = struct {
	sync.Mutex
	entries map[string]clusterCacheEntry
}{entries: map[string]clusterCacheEntry{}}

type clusterCacheEntry struct {
	value any
	until time.Time
}

func clusterCacheGet(key string) (any, bool) {
	clusterCache.Lock()
	defer clusterCache.Unlock()
	e, ok := clusterCache.entries[key]
	if !ok || time.Now().After(e.until) {
		return nil, false
	}
	return e.value, true
}

func clusterCachePut(key string, value any) {
	clusterCache.Lock()
	defer clusterCache.Unlock()
	clusterCache.entries[key] = clusterCacheEntry{value: value, until: time.Now().Add(clusterCacheTTL)}
}

// Cluster-level read-only endpoints: events feed, storage classes, the
// union of every Ingress host. The node and env-metrics surfaces live
// in their own files; this one is the slim "what's the cluster doing"
// strip.

// Events returns the recent Kubernetes Events in the requested namespace
// (defaults to KUSO_NAMESPACE). Newest first.
func (h *KubernetesHandler) Events(w http.ResponseWriter, r *http.Request) {
	ns := r.URL.Query().Get("namespace")
	if ns == "" {
		ns = h.Namespace
		if ns == "" {
			ns = "kuso"
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	events, err := h.Kube.Clientset.CoreV1().Events(ns).List(ctx, metav1.ListOptions{Limit: 200})
	if err != nil {
		h.Logger.Error("list events", "ns", ns, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	// Sort newest first by lastTimestamp (fall back to eventTime).
	sort.SliceStable(events.Items, func(i, j int) bool {
		ti := events.Items[i].LastTimestamp.Time
		tj := events.Items[j].LastTimestamp.Time
		if ti.IsZero() {
			ti = events.Items[i].EventTime.Time
		}
		if tj.IsZero() {
			tj = events.Items[j].EventTime.Time
		}
		return ti.After(tj)
	})
	writeJSON(w, http.StatusOK, events.Items)
}

// StorageClasses returns every storage class in the cluster.
//
// 30s TTL'd. StorageClasses change rarely (once at install, again
// only when an admin touches the cluster) but the cluster-overview
// tab polls them often.
func (h *KubernetesHandler) StorageClasses(w http.ResponseWriter, r *http.Request) {
	if cached, ok := clusterCacheGet("storage-classes"); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	scs, err := h.Kube.Clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list storage classes", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	clusterCachePut("storage-classes", scs.Items)
	writeJSON(w, http.StatusOK, scs.Items)
}

// Domains returns the union of every Ingress host currently configured
// in the cluster. Used by the project-create UI to warn about clashes.
//
// 30s TTL'd. Cluster-wide Ingress LIST is the heaviest query in this
// file (no informer, full TLS spec per item), and the project-create
// dialog polls this just to populate a "host already taken" hint.
func (h *KubernetesHandler) Domains(w http.ResponseWriter, r *http.Request) {
	if cached, ok := clusterCacheGet("ingress-domains"); ok {
		writeJSON(w, http.StatusOK, cached)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	ings, err := h.Kube.Clientset.NetworkingV1().Ingresses("").List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list ingresses", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	domains := map[string]struct{}{}
	for _, ing := range ings.Items {
		for _, rule := range ing.Spec.Rules {
			if rule.Host != "" {
				domains[rule.Host] = struct{}{}
			}
		}
	}
	out := make([]string, 0, len(domains))
	for d := range domains {
		out = append(out, d)
	}
	sort.Strings(out)
	clusterCachePut("ingress-domains", out)
	writeJSON(w, http.StatusOK, out)
}
