package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"kuso/server/internal/status"
	"kuso/server/internal/version"
)

// StatusHandler exposes /api/status, the public version surface the
// Vue UI's footer + the install probe both read.
type StatusHandler struct {
	Status *status.Service
	Logger *slog.Logger
}

// Handler returns an http.Handler that emits the {kuso, kubernetes,
// operator} versions plus an "ok" string. /healthz already covers
// liveness; /api/status is the authenticated counterpart for the UI.
func (h *StatusHandler) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		body := status.HealthzBody{Status: "ok", Version: version.Version()}
		if h.Status != nil {
			body = h.Status.Health(ctx, version.Version())
		}
		writeJSON(w, http.StatusOK, body)
	}
}
