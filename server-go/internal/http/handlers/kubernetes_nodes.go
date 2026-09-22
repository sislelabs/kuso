package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/sync/singleflight"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	restclient "k8s.io/client-go/rest"

	"kuso/server/internal/kube"
	"kuso/server/internal/nodeshape"
)

// Read-only views over the cluster nodes: list + per-node history.
// Lifecycle ops (join, validate, remove, label edits) live in
// kubernetes_node_lifecycle.go. The list-shape lives in
// internal/nodeshape/ — this file is just the adapter that gathers the
// kube inputs and hands them off.

// Nodes lists every cluster node with the bits the UI needs to show
// region/zone, roles, taint markers, Ready state, and live resource
// usage. Usage data comes from metrics-server via the raw REST API
// — we don't pull in the metrics client-go package because that
// would add ~20MB of vendored deps for a single map lookup.
func (h *KubernetesHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	nodeList, err := h.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list nodes", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	// Pod count per node — served from the shared Pod informer
	// (indexer keyed on Spec.NodeName). On a cold cache (server boot)
	// we fall back to a single cluster-wide LIST so the UI never sees
	// a transient zero-pods view.
	podsByNode, ok := h.Kube.Cache.PodCountsByNode()
	if !ok {
		allPods, _ := h.Kube.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
		podsByNode = map[string]int{}
		if allPods != nil {
			for _, p := range allPods.Items {
				podsByNode[p.Spec.NodeName]++
			}
		}
	}
	// Live CPU/memory from metrics-server. Failure is non-fatal —
	// metrics-server is optional in some clusters; we just leave
	// usage fields at 0 when it's unavailable.
	usage := nodeMetrics(ctx, h.Kube)
	out := nodeshape.BuildSummaries(nodeList.Items, podsByNode, usage)
	writeJSON(w, http.StatusOK, out)
}

// NodeHistory returns up-to-7-days of resource samples for a node so
// the UI can render CPU/RAM/Disk sparklines on the drill-down. The
// sampler goroutine writes one row per node per 30 min — see
// internal/nodemetrics. Empty array is a valid response (sampler
// hasn't ticked yet, or this node was just added).
func (h *KubernetesHandler) NodeHistory(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, "metrics history not wired")
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		writeErr(w, http.StatusBadRequest, "node name required")
		return
	}
	// `since` defaults to 7 days; cap any user-supplied window at 7d
	// so we don't accidentally serve a denial-of-service-grade query.
	hours := 24 * 7
	if q := r.URL.Query().Get("hours"); q != "" {
		if v, err := strconv.Atoi(q); err == nil && v > 0 && v <= 24*7 {
			hours = v
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	rows, err := h.DB.ListNodeMetrics(ctx, name, time.Now().Add(-time.Duration(hours)*time.Hour))
	if err != nil {
		h.Logger.Error("node history", "node", name, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"node":    name,
		"samples": rows,
	})
}

// nodeMetrics fetches metrics.k8s.io/v1beta1/nodes via the discovery
// REST client. Returns name → usage. Empty map on any failure —
// cluster monitoring shouldn't be a hard dependency for the nodes
// list (metrics-server is optional in some k3s installs).
// nodeMetrics returns per-node live usage, cached for nodeMetricsTTL.
//
// The nodes page polls every 5s and every open tab issues its own
// request, so without a cache N viewers meant N metrics-server hits per
// 5s window on top of everything else already querying it. Same
// reasoning (and same TTL) as the pod-metrics cache in
// metrics_cache.go; metrics.k8s.io has no watch verb, so a short TTL is
// the only option. Errors are not cached — the caller renders zeros.
func nodeMetrics(ctx context.Context, kc *kube.Client) map[string]nodeshape.Usage {
	if kc == nil || kc.Clientset == nil {
		return map[string]nodeshape.Usage{}
	}
	nodeMetricsCache.mu.RLock()
	e := nodeMetricsCache.entry
	nodeMetricsCache.mu.RUnlock()
	if e.usage != nil && time.Since(e.fetched) < nodeMetricsTTL {
		return e.usage
	}
	v, err, _ := nodeMetricsCache.sf.Do("nodes", func() (any, error) {
		nodeMetricsCache.mu.RLock()
		e := nodeMetricsCache.entry
		nodeMetricsCache.mu.RUnlock()
		if e.usage != nil && time.Since(e.fetched) < nodeMetricsTTL {
			return e.usage, nil
		}
		u := fetchNodeMetrics(ctx, kc)
		nodeMetricsCache.mu.Lock()
		nodeMetricsCache.entry = nodeMetricsEntry{usage: u, fetched: time.Now()}
		nodeMetricsCache.mu.Unlock()
		return u, nil
	})
	if err != nil {
		return map[string]nodeshape.Usage{}
	}
	if u, ok := v.(map[string]nodeshape.Usage); ok {
		return u
	}
	return map[string]nodeshape.Usage{}
}

// nodeMetricsTTL matches podMetricsTTL: below the UI's 5s poll and
// below metrics-server's own scrape interval, so the cache is never the
// dominant source of staleness.
const nodeMetricsTTL = 5 * time.Second

type nodeMetricsEntry struct {
	usage   map[string]nodeshape.Usage
	fetched time.Time
}

var nodeMetricsCache = struct {
	mu    sync.RWMutex
	entry nodeMetricsEntry
	sf    singleflight.Group
}{}

// ResetNodeMetricsCacheForTesting drops the cached node usage.
func ResetNodeMetricsCacheForTesting() {
	nodeMetricsCache.mu.Lock()
	nodeMetricsCache.entry = nodeMetricsEntry{}
	nodeMetricsCache.mu.Unlock()
}

// fetchNodeMetrics is the uncached read against metrics.k8s.io.
func fetchNodeMetrics(ctx context.Context, kc *kube.Client) map[string]nodeshape.Usage {
	out := map[string]nodeshape.Usage{}
	if kc == nil || kc.Clientset == nil {
		return out
	}
	rest := kc.Clientset.Discovery().RESTClient()
	if rest == nil {
		return out
	}
	mctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := rest.Get().AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").DoRaw(mctx)
	if err != nil {
		return out
	}
	var resp struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Usage struct {
				CPU    string `json:"cpu"`    // "<n>n" or "<n>m" — we coerce to milli-CPU
				Memory string `json:"memory"` // "<n>Ki|Mi|Gi"
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return out
	}
	for _, it := range resp.Items {
		out[it.Metadata.Name] = nodeshape.Usage{
			CPUMilli: parseCPU(it.Usage.CPU),
			MemBytes: parseQuantity(it.Usage.Memory),
		}
	}
	// Disk comes from each kubelet's Summary API, not from the node
	// object: ephemeral-storage Capacity minus Allocatable is a fixed
	// kubelet reservation, so it renders a constant ~5% no matter how
	// full the disk really is. One request per node, behind the same
	// cache as the block above, and best-effort — a node that doesn't
	// answer keeps the static fallback in nodeshape.
	for name, u := range out {
		fs, err := nodeFilesystem(ctx, rest, name)
		if err != nil {
			continue
		}
		u.DiskCapacityBytes = fs.capacity
		u.DiskAvailableBytes = fs.available
		out[name] = u
	}
	return out
}

type nodeFS struct{ capacity, available int64 }

// nodeFilesystem reads one node's real root-filesystem usage from the
// kubelet Summary API, proxied through the apiserver.
func nodeFilesystem(ctx context.Context, rest restclient.Interface, node string) (nodeFS, error) {
	fctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	body, err := rest.Get().
		AbsPath(fmt.Sprintf("/api/v1/nodes/%s/proxy/stats/summary", node)).
		DoRaw(fctx)
	if err != nil {
		return nodeFS{}, err
	}
	var resp struct {
		Node struct {
			FS *struct {
				CapacityBytes  *int64 `json:"capacityBytes"`
				AvailableBytes *int64 `json:"availableBytes"`
				UsedBytes      *int64 `json:"usedBytes"`
			} `json:"fs"`
		} `json:"node"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nodeFS{}, err
	}
	fs := resp.Node.FS
	if fs == nil || fs.CapacityBytes == nil || *fs.CapacityBytes <= 0 {
		return nodeFS{}, errors.New("summary has no node fs capacity")
	}
	out := nodeFS{capacity: *fs.CapacityBytes}
	switch {
	case fs.AvailableBytes != nil:
		out.available = *fs.AvailableBytes
	case fs.UsedBytes != nil:
		out.available = out.capacity - *fs.UsedBytes
	default:
		return nodeFS{}, errors.New("summary has neither available nor used bytes")
	}
	return out, nil
}
