package handlers

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/uptime"
)

// UptimeHandler serves the read side of uptime checks. The opt-out and
// path are written through the project and service PATCH endpoints.
type UptimeHandler struct {
	DB      *db.DB
	Cluster uptime.Cluster
	Logger  *slog.Logger
}

func (h *UptimeHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/uptime", h.ProjectStatus)
}

// ProjectStatus returns one entry per production web service.
func (h *UptimeHandler) ProjectStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	targets, err := h.Cluster.Targets(ctx, project)
	if err != nil {
		h.Logger.Error("uptime status: read targets", "project", project, "err", err)
		writeErr(w, http.StatusInternalServerError, "read uptime targets")
		return
	}
	rows, err := h.DB.ListUptimeStates(ctx, project)
	if err != nil {
		h.Logger.Error("uptime status: read state", "project", project, "err", err)
		writeErr(w, http.StatusInternalServerError, "read uptime state")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  !uptime.Disabled(),
		"services": uptime.Status(project, targets, rows, time.Now()),
	})
}
