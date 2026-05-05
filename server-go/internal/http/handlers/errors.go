package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// ErrorsHandler exposes /api/projects/{project}/services/{service}/errors.
// Reads the ErrorEvent table populated by the errorscan goroutine and
// returns groups (one row per fingerprint).
type ErrorsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the route. JWT-protected — the caller's project
// access is verified against requireProjectAccess.
func (h *ErrorsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/errors", h.List)
}

// List returns aggregated error groups. Query params:
//
//	?since=24h   — lookback window. Default 24h, max 30d.
//	?limit=50    — max groups returned (1–200, default 50).
func (h *ErrorsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	since := time.Now().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			if d > 30*24*time.Hour {
				d = 30 * 24 * time.Hour
			}
			since = time.Now().Add(-d)
		}
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	groups, err := h.DB.ListErrorGroups(ctx, project, service, since, limit)
	if err != nil {
		h.Logger.Error("errors: list", "err", err, "project", project, "service", service)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
