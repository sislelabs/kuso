package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"sort"
	"time"

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
}) {
	rt.Get("/api/kubernetes/events", h.Events)
	rt.Get("/api/kubernetes/storageclasses", h.StorageClasses)
	rt.Get("/api/kubernetes/domains", h.Domains)
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

// _ = networkingv1 keeps the import alive even if the reflection path
// isn't visible to a static analyser; without it the import would look
// unused on go vet.
var _ = networkingv1.Ingress{}
