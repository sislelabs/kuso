package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/builds"
)

// BuildsHandler exposes the build list + trigger routes for a service.
type BuildsHandler struct {
	Svc    *builds.Service
	Logger *slog.Logger
}

// Mount registers builds routes onto the given chi router.
func (h *BuildsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/builds", h.List)
	r.Post("/api/projects/{project}/services/{service}/builds", h.Create)
}

func buildsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// List returns the builds for a service, newest first.
func (h *BuildsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	out, err := h.Svc.List(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "list builds", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Create triggers a new build for the service. Body: {branch?, ref?}.
func (h *BuildsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req builds.CreateBuildRequest
	// Empty body is legitimate: caller wants a build of the default
	// branch with synthetic ref.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := buildsCtx(r)
	defer cancel()
	out, err := h.Svc.Create(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req)
	if err != nil {
		h.fail(w, "create build", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *BuildsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, builds.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, builds.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("builds handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
