package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/envisolation"
)

// EnvIsolationHandler exposes the ISO-3 env-isolation switch. Enabling
// changes which pods may reach production, so it is admin-only and
// refused until every env pod carries its env label.
type EnvIsolationHandler struct {
	DB     *db.DB
	Svc    *envisolation.Service
	Logger *slog.Logger
}

func (h *EnvIsolationHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/env-isolation", h.Get)
	r.Post("/api/projects/{project}/env-isolation/stamp", h.Stamp)
	r.Put("/api/projects/{project}/env-isolation", h.Set)
}

func (h *EnvIsolationHandler) Get(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(r.Context(), w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	rep, err := h.Svc.Readiness(r.Context(), project)
	if err != nil {
		h.fail(w, project, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *EnvIsolationHandler) Stamp(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(r.Context(), w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	stamped, err := h.Svc.Stamp(r.Context(), project)
	if err != nil {
		h.fail(w, project, err)
		return
	}
	if stamped == nil {
		stamped = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"stamped": stamped})
}

func (h *EnvIsolationHandler) Set(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(r.Context(), w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	var in struct {
		Enabled *bool `json:"enabled"`
		// NonProductionEnvs, when present, also sets ISO-4 isolation
		// between non-production envs.
		NonProductionEnvs *bool `json:"nonProductionEnvs"`
	}
	if err := decodeJSON(w, r, &in); err != nil || in.Enabled == nil {
		writeErr(w, http.StatusBadRequest, `body must be {"enabled": true|false, "nonProductionEnvs"?: true|false}`)
		return
	}
	rep, err := h.Svc.SetEnabled(r.Context(), project, *in.Enabled, in.NonProductionEnvs)
	if errors.Is(err, envisolation.ErrNotReady) {
		writeErrExtra(w, http.StatusConflict, err.Error(), "env_isolation_not_ready", map[string]any{"readiness": rep})
		return
	}
	if err != nil {
		h.fail(w, project, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (h *EnvIsolationHandler) fail(w http.ResponseWriter, project string, err error) {
	if errors.Is(err, envisolation.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "project not found")
		return
	}
	h.Logger.Error("env isolation", "project", project, "err", err)
	writeErr(w, http.StatusInternalServerError, "env isolation: internal error")
}
