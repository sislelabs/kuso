package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/logs"
)

// LogsHandler exposes the log tail route.
type LogsHandler struct {
	Svc    *logs.Service
	Logger *slog.Logger
}

// Mount registers logs routes onto the given chi router.
func (h *LogsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/logs", h.Tail)
}

func logsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

// Tail is GET /api/projects/{project}/services/{service}/logs
//
// Query params: env=<name> (default production), lines=<N> (default 200,
// max 2000).
func (h *LogsHandler) Tail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env := q.Get("env")
	lines := 200
	if n, err := strconv.Atoi(q.Get("lines")); err == nil && n > 0 {
		lines = n
	}
	ctx, cancel := logsCtx(r)
	defer cancel()
	out, envName, err := h.Svc.Tail(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env, lines)
	if err != nil {
		switch {
		case errors.Is(err, logs.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("tail logs", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": chi.URLParam(r, "project"),
		"service": chi.URLParam(r, "service"),
		"env":     envName,
		"lines":   out,
	})
}
