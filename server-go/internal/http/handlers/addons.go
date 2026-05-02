package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/addons"
)

// AddonsHandler exposes the /api/projects/:p/addons routes.
type AddonsHandler struct {
	Svc    *addons.Service
	Logger *slog.Logger
}

// Mount registers the routes onto the given chi router.
func (h *AddonsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/addons", h.List)
	r.Post("/api/projects/{project}/addons", h.Add)
	r.Delete("/api/projects/{project}/addons/{addon}", h.Delete)
}

func addonsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (h *AddonsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	out, err := h.Svc.List(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list addons", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *AddonsHandler) Add(w http.ResponseWriter, r *http.Request) {
	var req addons.CreateAddonRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	out, err := h.Svc.Add(ctx, chi.URLParam(r, "project"), req)
	if err != nil {
		h.fail(w, "add addon", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *AddonsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if err := h.Svc.Delete(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "delete addon", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AddonsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, addons.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, addons.ErrConflict):
		http.Error(w, "conflict", http.StatusConflict)
	case errors.Is(err, addons.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("addons handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
