package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		http.Error(w, "internal", http.StatusInternalServerError)
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
		http.Error(w, "metrics history not wired", http.StatusServiceUnavailable)
		return
	}
	name := chi.URLParam(r, "name")
	if name == "" {
		http.Error(w, "node name required", http.StatusBadRequest)
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
		http.Error(w, "internal", http.StatusInternalServerError)
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
func nodeMetrics(ctx context.Context, kc *kube.Client) map[string]nodeshape.Usage {
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
	return out
}
