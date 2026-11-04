package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/logs"
)

// LogsHandler exposes the log tail route.
type LogsHandler struct {
	Svc    *logs.Service
	DB     *db.DB
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
// max 2000), sinceTime=<RFC3339Nano> (env=run:<name> only: return the oldest
// N lines at or after it instead of the newest N; each line carries "ts").
func (h *LogsHandler) Tail(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	env := q.Get("env")
	lines := 200
	if n, err := strconv.Atoi(q.Get("lines")); err == nil && n > 0 {
		// Cap to keep an authenticated user from sending ?lines=10M
		// and OOM-ing the server while we buffer pod logs in memory.
		if n > 2000 {
			n = 2000
		}
		lines = n
	}
	var since time.Time
	if raw := q.Get("sinceTime"); raw != "" {
		t, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "sinceTime must be RFC3339")
			return
		}
		if !strings.HasPrefix(env, "run:") {
			writeErr(w, http.StatusBadRequest, "sinceTime is only supported for env=run:<name>")
			return
		}
		since = t
	}
	ctx, cancel := logsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, envName, err := h.Svc.TailSince(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env, lines, since)
	if err != nil {
		switch {
		case errors.Is(err, logs.ErrNotFound):
			msg := h.Svc.MissingTarget(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env)
			if notice, ok := h.Svc.BuildLogExpired(ctx, chi.URLParam(r, "project"), env); ok {
				msg = notice
			}
			writeErr(w, http.StatusNotFound, msg)
		default:
			h.Logger.Error("tail logs", "err", err)
			writeErr(w, http.StatusInternalServerError, "internal")
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
