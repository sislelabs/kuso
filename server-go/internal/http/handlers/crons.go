// Cron HTTP handler. Routes hang off /api/projects/{p}/services/{s}/crons
// so a cron is always discoverable from its parent service. The list
// endpoint at the project level (/api/projects/{p}/crons) is the
// project-wide rollup the dashboard uses.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/crons"
	"kuso/server/internal/db"
)

type CronsHandler struct {
	Svc    *crons.Service
	DB     *db.DB
	Logger *slog.Logger
}

func (h *CronsHandler) Mount(r chi.Router) {
	// Project-wide rollup.
	r.Get("/api/projects/{project}/crons", h.ListForProject)
	// Per-service surface.
	r.Get("/api/projects/{project}/services/{service}/crons", h.List)
	r.Post("/api/projects/{project}/services/{service}/crons", h.Add)
	r.Get("/api/projects/{project}/services/{service}/crons/{name}", h.Get)
	r.Patch("/api/projects/{project}/services/{service}/crons/{name}", h.Update)
	r.Delete("/api/projects/{project}/services/{service}/crons/{name}", h.Delete)
	r.Post("/api/projects/{project}/services/{service}/crons/{name}/sync", h.Sync)
}

func (h *CronsHandler) Sync(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.SyncFromService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "sync cron", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func cronsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (h *CronsHandler) ListForProject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.List(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list crons", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CronsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListForService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "list crons", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CronsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.Get(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "get cron", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CronsHandler) Add(w http.ResponseWriter, r *http.Request) {
	var req crons.CreateCronRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.Add(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req)
	if err != nil {
		h.fail(w, "add cron", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *CronsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req crons.UpdateCronRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "name"), req)
	if err != nil {
		h.fail(w, "update cron", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *CronsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cronsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	if err := h.Svc.Delete(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "name")); err != nil {
		h.fail(w, "delete cron", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CronsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, crons.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, crons.ErrConflict):
		// Pass the wrapped error through so the UI shows
		// "cron foo/bar already exists" not bare "409 Conflict".
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, crons.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("crons handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
