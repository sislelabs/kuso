package handlers

import (
	"net/http"

	"kuso/server/internal/pkgupdates"
)

// NodeUpdates returns the per-node host package-update advisory the
// pkg-probe DaemonSet writes to node annotations. Admin-only — it's
// node/infra state. Read-only: builds a transient pkgupdates.Watcher
// (no DB/Notify needed for the surface) and lists.
//
// GET /api/kubernetes/nodes/updates → {"data":[Advisory,...]}
func (h *KubernetesHandler) NodeUpdates(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		http.Error(w, "node updates unavailable: no kube client", http.StatusServiceUnavailable)
		return
	}
	pw := &pkgupdates.Watcher{Kube: h.Kube}
	advisories, err := pw.List(r.Context())
	if err != nil {
		http.Error(w, "list node updates: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": advisories})
}
