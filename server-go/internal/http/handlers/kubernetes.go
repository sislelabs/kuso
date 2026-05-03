package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	networkingv1 "k8s.io/api/networking/v1"

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
}) {
	rt.Get("/api/kubernetes/events", h.Events)
	rt.Get("/api/kubernetes/storageclasses", h.StorageClasses)
	rt.Get("/api/kubernetes/domains", h.Domains)
	rt.Get("/api/kubernetes/nodes", h.Nodes)
	rt.Patch("/api/kubernetes/nodes/{name}/taints", h.SetNodeTaints)
	rt.Patch("/api/kubernetes/nodes/{name}/labels", h.SetNodeLabels)
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

// nodeSummary is the slim wire shape the UI needs. Drops every Node
// field the dashboard never reads (system info, allocatable, conditions
// detail) so the response stays small. The taint shape mirrors the
// API exactly so a PATCH round-trips without a translation layer.
type nodeSummary struct {
	Name        string            `json:"name"`
	Ready       bool              `json:"ready"`
	Roles       []string          `json:"roles"`
	Region      string            `json:"region,omitempty"`
	Zone        string            `json:"zone,omitempty"`
	Labels      map[string]string `json:"labels"`
	Taints      []nodeTaint       `json:"taints"`
	Schedulable bool              `json:"schedulable"`
	CreatedAt   string            `json:"createdAt,omitempty"`
}

type nodeTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value,omitempty"`
	Effect string `json:"effect"` // NoSchedule | PreferNoSchedule | NoExecute
}

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
		zone := n.Labels["topology.kubernetes.io/zone"]
		taints := make([]nodeTaint, 0, len(n.Spec.Taints))
		for _, t := range n.Spec.Taints {
			taints = append(taints, nodeTaint{Key: t.Key, Value: t.Value, Effect: string(t.Effect)})
		}
		out = append(out, nodeSummary{
			Name:        n.Name,
			Ready:       ready,
			Roles:       roles,
			Region:      region,
			Zone:        zone,
			Labels:      n.Labels,
			Taints:      taints,
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

// SetNodeTaints replaces a node's spec.taints with the body's list.
// Body shape mirrors nodeTaint above. A typical kuso flow drops a
// "kuso.sislelabs.com/region=eu:NoSchedule" taint so workloads only
// land here when they tolerate it.
func (h *KubernetesHandler) SetNodeTaints(w http.ResponseWriter, r *http.Request) {
	name := chiURLParam(r, "name")
	if name == "" {
		http.Error(w, "missing node name", http.StatusBadRequest)
		return
	}
	var body struct {
		Taints []nodeTaint `json:"taints"`
	}
	if err := decodeJSON(r, &body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := kubeCtx(r)
	defer cancel()
	patch, err := nodeTaintsPatch(body.Taints)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/strategic-merge-patch+json", patch, metav1.PatchOptions{},
	); err != nil {
		h.Logger.Error("patch node taints", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetNodeLabels merges the body's labels onto a node. Used to drop a
// "kuso.sislelabs.com/region: eu" label so the dashboard's region
// picker can group nodes without touching taints.
func (h *KubernetesHandler) SetNodeLabels(w http.ResponseWriter, r *http.Request) {
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
	ctx, cancel := kubeCtx(r)
	defer cancel()
	patch, err := nodeLabelsPatch(body.Labels)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := h.Kube.Clientset.CoreV1().Nodes().Patch(
		ctx, name, "application/merge-patch+json", patch, metav1.PatchOptions{},
	); err != nil {
		h.Logger.Error("patch node labels", "node", name, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// nodeTaintsPatch builds a strategic-merge-patch payload that REPLACES
// spec.taints with the given list. Strategic merge needs the array to
// be tagged with the merge strategy because taints aren't a primitive
// list — using a JSON merge would only add taints, never remove any.
// We sidestep that by sending the explicit "$retainKeys" directive.
func nodeTaintsPatch(taints []nodeTaint) ([]byte, error) {
	for i, t := range taints {
		if t.Key == "" {
			return nil, fmt.Errorf("taint %d: key required", i)
		}
		switch t.Effect {
		case "NoSchedule", "PreferNoSchedule", "NoExecute":
		default:
			return nil, fmt.Errorf("taint %d: effect must be NoSchedule|PreferNoSchedule|NoExecute (got %q)", i, t.Effect)
		}
	}
	if taints == nil {
		taints = []nodeTaint{}
	}
	patch := map[string]any{
		"spec": map[string]any{
			"$retainKeys": []string{"taints"},
			"taints":      taints,
		},
	}
	return json.Marshal(patch)
}

// nodeLabelsPatch builds a JSON merge-patch that adds/overwrites the
// given labels. Setting a label's value to "" deletes it, matching
// kubectl label foo-.
func nodeLabelsPatch(labels map[string]string) ([]byte, error) {
	if len(labels) == 0 {
		return nil, errors.New("no labels to patch")
	}
	out := map[string]any{}
	for k, v := range labels {
		if v == "" {
			out[k] = nil
		} else {
			out[k] = v
		}
	}
	return json.Marshal(map[string]any{"metadata": map[string]any{"labels": out}})
}

// _ = networkingv1 keeps the import alive even if the reflection path
// isn't visible to a static analyser; without it the import would look
// unused on go vet.
var _ = networkingv1.Ingress{}
