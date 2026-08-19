package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

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
		writeErr(w, http.StatusServiceUnavailable, "node updates unavailable: no kube client")
		return
	}
	pw := &pkgupdates.Watcher{Kube: h.Kube}
	advisories, err := pw.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list node updates: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": advisories})
}

// ApplyNodeUpdates launches the per-node patch Job. Admin-only — it
// mutates the host OS and may (with allowReboot) reboot the node.
//
// POST /api/kubernetes/nodes/{name}/apply-updates  {"allowReboot":bool}
func (h *KubernetesHandler) ApplyNodeUpdates(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	if h.Kube == nil {
		writeErr(w, http.StatusServiceUnavailable, "node updates unavailable: no kube client")
		return
	}
	node := chi.URLParam(r, "name")
	var body struct {
		AllowReboot bool `json:"allowReboot"`
	}
	// Empty body is fine → allowReboot defaults false (patch-only).
	_ = json.NewDecoder(r.Body).Decode(&body)

	pw := &pkgupdates.Watcher{Kube: h.Kube, Logger: h.Logger}
	err := pw.Apply(r.Context(), node, body.AllowReboot)
	switch {
	case errors.Is(err, pkgupdates.ErrNothingToDo):
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, pkgupdates.ErrAlreadyRunning):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "apply updates: "+err.Error())
	default:
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "started", "node": node, "allowReboot": body.AllowReboot})
	}
}
