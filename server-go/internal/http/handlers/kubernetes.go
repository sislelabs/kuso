package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"kuso/server/internal/kube"
)

// KubernetesHandler exposes /api/kubernetes/* — events, storage classes,
// and the domains the cluster already advertises via Ingress.
type KubernetesHandler struct {
	Kube      *kube.Client
	Namespace string
	Logger    *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *KubernetesHandler) Mount(rt interface {
	Get(string, http.HandlerFunc)
	Post(string, http.HandlerFunc)
	Patch(string, http.HandlerFunc)
	Put(string, http.HandlerFunc)
}) {
	rt.Get("/api/kubernetes/events", h.Events)
	rt.Get("/api/kubernetes/storageclasses", h.StorageClasses)
	rt.Get("/api/kubernetes/domains", h.Domains)
	rt.Get("/api/kubernetes/nodes", h.Nodes)
	// Single endpoint, simple semantics: replace the kuso labels for
	// this node with the body. Server expands kuso conventions (region
	// → matching NoSchedule taint) under the hood.
	rt.Put("/api/kubernetes/nodes/{name}/labels", h.PutNodeLabels)
	// Per-env CPU + mem snapshot. Reads from the metrics.k8s.io
	// metrics-server API. Returns one entry per pod in the env.
	rt.Get("/api/kubernetes/envs/{env}/metrics", h.EnvMetrics)
}

// envMetricsResponse is the JSON wire shape: a list of per-pod
// resource snapshots plus the timestamp the metrics-server emitted.
type envMetricsResponse struct {
	Env    string         `json:"env"`
	Window string         `json:"window,omitempty"`
	Pods   []podMetricRow `json:"pods"`
}

type podMetricRow struct {
	Pod       string `json:"pod"`
	Timestamp string `json:"timestamp,omitempty"`
	CPUm      int64  `json:"cpuMillicores"`
	MemBytes  int64  `json:"memBytes"`
}

// EnvMetrics returns the current CPU + memory usage for every pod in
// the named env. Sources from metrics.k8s.io/v1beta1 PodMetrics via
// the dynamic client (avoids pulling in k8s.io/metrics as a separate
// module dep). Returns an empty list when metrics-server isn't
// installed or the env has no pods yet.
func (h *KubernetesHandler) EnvMetrics(w http.ResponseWriter, r *http.Request) {
	envName := chi.URLParam(r, "env")
	if envName == "" {
		http.Error(w, "missing env", http.StatusBadRequest)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()

	gvr := schema.GroupVersionResource{
		Group:    "metrics.k8s.io",
		Version:  "v1beta1",
		Resource: "pods",
	}
	list, err := h.Kube.Dynamic.Resource(gvr).Namespace(h.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/instance=" + envName,
	})
	if err != nil {
		// metrics-server not installed → return empty so the UI shows
		// a "no metrics yet" state rather than an error banner.
		writeJSON(w, http.StatusOK, envMetricsResponse{Env: envName, Pods: []podMetricRow{}})
		return
	}
	out := envMetricsResponse{Env: envName, Pods: make([]podMetricRow, 0, len(list.Items))}
	for i := range list.Items {
		item := list.Items[i].Object
		row := podMetricRow{Pod: list.Items[i].GetName()}
		if ts, ok := item["timestamp"].(string); ok {
			row.Timestamp = ts
		}
		if win, ok := item["window"].(string); ok && out.Window == "" {
			out.Window = win
		}
		// Sum across containers in the pod. The metrics-server
		// emits one entry per container under .containers[].usage.
		if containers, ok := item["containers"].([]any); ok {
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
					row.CPUm += parseCPU(cpu)
				}
				if mem, ok := usage["memory"].(string); ok {
					row.MemBytes += parseQuantity(mem)
				}
			}
		}
		out.Pods = append(out.Pods, row)
	}
	writeJSON(w, http.StatusOK, out)
}

// parseCPU returns CPU in millicores. Accepts kube quantity formats
// like "100m", "0.5", "1". Anything else returns 0 (the panel just
// shows "—").
func parseCPU(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if strings.HasSuffix(s, "n") {
		// nanocores → millicores
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "n"), 10, 64)
		return n / 1_000_000
	}
	if strings.HasSuffix(s, "u") {
		// microcores
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "u"), 10, 64)
		return n / 1_000
	}
	if strings.HasSuffix(s, "m") {
		n, _ := strconv.ParseInt(strings.TrimSuffix(s, "m"), 10, 64)
		return n
	}
	// Treat as whole cores.
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f * 1000)
	}
	return 0
}

// parseQuantity returns bytes from a kube quantity string. Handles
// the common Ki/Mi/Gi suffixes plus plain bytes. Anything else → 0.
func parseQuantity(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	mults := []struct {
		suf string
		mul int64
	}{
		{"Ei", 1 << 60}, {"Pi", 1 << 50}, {"Ti", 1 << 40},
		{"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10},
		{"E", 1_000_000_000_000_000_000}, {"P", 1_000_000_000_000_000},
		{"T", 1_000_000_000_000}, {"G", 1_000_000_000},
		{"M", 1_000_000}, {"k", 1_000},
	}
	for _, m := range mults {
		if strings.HasSuffix(s, m.suf) {
			n, _ := strconv.ParseInt(strings.TrimSuffix(s, m.suf), 10, 64)
			return n * m.mul
		}
	}
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func kubeCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

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
func (h *KubernetesHandler) StorageClasses(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := kubeCtx(r)
	defer cancel()
	scs, err := h.Kube.Clientset.StorageV1().StorageClasses().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list storage classes", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, scs.Items)
}

// Domains returns the union of every Ingress host currently configured
// in the cluster. Used by the project-create UI to warn about clashes.
func (h *KubernetesHandler) Domains(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, out)
}

// nodeSummary is the slim wire shape the UI needs. Only kuso-managed
// labels are exposed — the underlying kube/k3s labels stay hidden so
// users don't accidentally edit topology.kubernetes.io/* and break the
// scheduler. Taints aren't surfaced at all; the labels endpoint
// derives them from convention (region → NoSchedule).
type nodeSummary struct {
	Name        string            `json:"name"`
	Ready       bool              `json:"ready"`
	Roles       []string          `json:"roles"`
	// Region/Zone read from the upstream topology labels for display
	// only; not editable through the UI.
	Region      string            `json:"region,omitempty"`
	Zone        string            `json:"zone,omitempty"`
	// KusoLabels is the editable surface — only labels under the
	// kuso.sislelabs.com/ namespace, with the prefix stripped on the
	// way out and re-applied on the way in.
	KusoLabels  map[string]string `json:"kusoLabels"`
	Schedulable bool              `json:"schedulable"`
	CreatedAt   string            `json:"createdAt,omitempty"`
}

const kusoLabelPrefix = "kuso.sislelabs.com/"

// Nodes lists every cluster node with the bits the UI needs to show
// region/zone, roles, taint markers, and Ready state.
func (h *KubernetesHandler) Nodes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := kubeCtx(r)
	defer cancel()
	nodes, err := h.Kube.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		h.Logger.Error("list nodes", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	out := make([]nodeSummary, 0, len(nodes.Items))
	for i := range nodes.Items {
		n := &nodes.Items[i]
		ready := false
		for _, c := range n.Status.Conditions {
			if c.Type == "Ready" && c.Status == "True" {
				ready = true
				break
			}
		}
		roles := nodeRoles(n.Labels)
		region := n.Labels["topology.kubernetes.io/region"]
		if region == "" {
			region = n.Labels[kusoLabelPrefix+"region"]
		}
		zone := n.Labels["topology.kubernetes.io/zone"]
		// Only kuso-namespaced labels are editable through the UI.
		kusoLabels := map[string]string{}
		for k, v := range n.Labels {
			if len(k) > len(kusoLabelPrefix) && k[:len(kusoLabelPrefix)] == kusoLabelPrefix {
				kusoLabels[k[len(kusoLabelPrefix):]] = v
			}
		}
		out = append(out, nodeSummary{
			Name:        n.Name,
			Ready:       ready,
			Roles:       roles,
			Region:      region,
			Zone:        zone,
			KusoLabels:  kusoLabels,
			Schedulable: !n.Spec.Unschedulable,
			CreatedAt:   n.CreationTimestamp.Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, out)
}

// nodeRoles infers the role names the UI surfaces (control-plane,
// worker, etc.) from the well-known kubernetes.io/role/* labels k3s and
// kubeadm both set.
func nodeRoles(labels map[string]string) []string {
	const prefix = "node-role.kubernetes.io/"
	out := []string{}
	for k := range labels {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k[len(prefix):])
		}
	}
	if len(out) == 0 {
		out = append(out, "worker")
	}
	sort.Strings(out)
	return out
}

// PutNodeLabels replaces the kuso-managed labels for a node. Body is
// {labels: {key: value}} — bare keys (no namespace prefix). The
// server applies the kuso.sislelabs.com/ prefix on the way in and
// strips it on the way out via Nodes() so the user never sees the
// namespace mechanics.
//
// Convention: when the user sets `region`, the server also drops a
// matching NoSchedule taint so workloads without a matching
// toleration won't land here. Removing the region label removes
// the matching taint. Other labels are pure metadata for now;
// future placement logic can pin services to specific
// labels via spec.placement.
func (h *KubernetesHandler) PutNodeLabels(w http.ResponseWriter, r *http.Request) {
	name := chiURLParam(r, "name")
	if name == "" {
		http.Error(w, "missing node name", http.StatusBadRequest)
		return
	}
	var body struct {
		Labels map[string]string `json:"labels"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Labels == nil {
		body.Labels = map[string]string{}
	}
	for k := range body.Labels {
		if k == "" {
			http.Error(w, "label key cannot be empty", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()

	// Read the live node so we can compute the diff (which kuso labels
	// went away) and translate that into a minimal label patch +
	// matching taint diff.
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		h.Logger.Error("get node for label put", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Step 1 — labels patch. Set every key in body; delete any kuso-
	// namespaced label that's no longer in body. JSON merge-patch
	// uses null to delete a key.
	desired := map[string]any{}
	for k, v := range body.Labels {
		desired[kusoLabelPrefix+k] = v
	}
	for k := range live.Labels {
		if len(k) > len(kusoLabelPrefix) && k[:len(kusoLabelPrefix)] == kusoLabelPrefix {
			short := k[len(kusoLabelPrefix):]
			if _, keep := body.Labels[short]; !keep {
				desired[k] = nil
			}
		}
	}
	labelPatch, err := json.Marshal(map[string]any{"metadata": map[string]any{"labels": desired}})
	if err != nil {
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/merge-patch+json", labelPatch, metav1.PatchOptions{},
	); err != nil {
		h.Logger.Error("patch node labels", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	// Step 2 — region-derived taint. Re-fetch so we see the new label
	// state, then ensure spec.taints carries exactly one
	// kuso.sislelabs.com/region=<value>:NoSchedule taint matching the
	// current label (or none, if the label is gone).
	if err := h.reconcileRegionTaint(ctx, name, body.Labels["region"]); err != nil {
		h.Logger.Warn("reconcile region taint", "node", name, "err", err)
	}

	w.WriteHeader(http.StatusNoContent)
}

// reconcileRegionTaint ensures spec.taints contains exactly one kuso
// region taint matching desiredValue (empty string = remove the taint).
// We send the full taint list so SMP doesn't merge — which is the
// only way to make a kube taint go away via patch.
func (h *KubernetesHandler) reconcileRegionTaint(ctx context.Context, name, desiredValue string) error {
	const taintKey = kusoLabelPrefix + "region"
	live, err := h.Kube.Clientset.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get: %w", err)
	}
	out := []map[string]any{}
	for _, t := range live.Spec.Taints {
		if t.Key == taintKey {
			continue
		}
		out = append(out, map[string]any{"key": t.Key, "value": t.Value, "effect": string(t.Effect), "timeAdded": t.TimeAdded})
	}
	if desiredValue != "" {
		out = append(out, map[string]any{
			"key":    taintKey,
			"value":  desiredValue,
			"effect": "NoSchedule",
		})
	}
	patch, err := json.Marshal(map[string]any{
		"spec": map[string]any{"$retainKeys": []string{"taints"}, "taints": out},
	})
	if err != nil {
		return err
	}
	_, err = h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/strategic-merge-patch+json", patch, metav1.PatchOptions{},
	)
	return err
}

// chiURLParam pulls a path param off the chi context. Wrapped so we
// don't dot-import chi from every handler.
func chiURLParam(r *http.Request, name string) string {
	return chi.URLParam(r, name)
}

// decodeJSON pulls the request body into out, capping at 1 MiB to
// avoid unbounded reads from a hostile client.
func decodeJSON(r *http.Request, out any) error {
	body := http.MaxBytesReader(nil, r.Body, 1<<20)
	defer body.Close()
	return json.NewDecoder(body).Decode(out)
}

// _ = networkingv1 keeps the import alive even if the reflection path
// isn't visible to a static analyser; without it the import would look
// unused on go vet.
var _ = networkingv1.Ingress{}

// _ = errors quiets the linter when no other site references it; it
// stays imported for future error-wrapping handlers below.
var _ = errors.New

