package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/updater"
)

// UpdaterHandler exposes the self-update endpoints. Read endpoints
// are open to any authenticated user — anyone can see "an update is
// available." Write (Start) is gated by perms in the router; we
// don't bake that into the handler so the gating is visible at the
// route table.
type UpdaterHandler struct {
	Svc    *updater.Service
	Logger *slog.Logger
}

func (h *UpdaterHandler) Mount(r chi.Router) {
	r.Get("/api/system/version", h.GetVersion)
	r.Post("/api/system/update", h.StartUpdate)
	r.Get("/api/system/update/status", h.GetStatus)
}

// GetVersion returns the cached state. We don't synchronously poll
// GH on every request — the background ticker keeps state fresh, and
// the UI can render a "last checked Xm ago" indicator with whatever
// it gets.
func (h *UpdaterHandler) GetVersion(w http.ResponseWriter, r *http.Request) {
	if h.Svc == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"current":     "",
			"needsUpdate": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, h.Svc.State())
}

// StartUpdate kicks the kube Job. Returns 202 immediately —
// completion is observable via GetStatus. Failures here are
// pre-flight (manifest missing, breaking change, etc); the actual
// rollout failures land in the status ConfigMap.
func (h *UpdaterHandler) StartUpdate(w http.ResponseWriter, r *http.Request) {
	if h.Svc == nil {
		http.Error(w, "updater unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	jobName, err := h.Svc.StartUpdate(ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job": jobName})
}

func (h *UpdaterHandler) GetStatus(w http.ResponseWriter, r *http.Request) {
	if h.Svc == nil {
		http.Error(w, "updater unavailable", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	st, ok := h.Svc.Status(ctx)
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"phase": ""})
		return
	}
	writeJSON(w, http.StatusOK, st)
}
