package handlers

import (
	"net/http"
	"sort"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

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
