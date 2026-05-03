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

	"kuso/server/internal/addons"
	"kuso/server/internal/kube"
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
	// Pin the addon's StatefulSet to a subset of nodes. PUT replaces
	// the placement struct verbatim; pass empty {} to clear.
	r.Put("/api/projects/{project}/addons/{addon}/placement", h.Placement)
	r.Get("/api/projects/{project}/addons/{addon}/secret-keys", h.SecretKeys)
	// Plaintext connection values. Gated behind secrets:read at the
	// router level so the autocomplete (keys-only) endpoint above
	// stays open to anyone with addons:read.
	r.Get("/api/projects/{project}/addons/{addon}/secret", h.Secret)
}

// Secret returns the addon's connection secret as a key→value map.
// Plaintext — gate at the router with secrets:read. Used by the addon
// overview's "Connection" panel so the user can copy DATABASE_URL etc.
// to connect from psql / their app / a tunnel.
func (h *AddonsHandler) Secret(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	values, err := h.Svc.SecretValues(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"))
	if err != nil {
		h.fail(w, "addon secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"values": values})
}

// SecretKeys is GET /api/projects/{project}/addons/{addon}/secret-keys.
// Lists keys in the addon's connection secret without ever exposing the
// values. Used by the frontend ${{ }} reference autocomplete.
func (h *AddonsHandler) SecretKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	keys, err := h.Svc.SecretKeys(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"))
	if err != nil {
		h.fail(w, "addon secret keys", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
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

// Placement updates spec.placement on a KusoAddon CR. Body shape
// matches kube.KusoPlacement: {labels: {…}, nodes: […]}. An empty
// body / null clears placement (schedule anywhere).
func (h *AddonsHandler) Placement(w http.ResponseWriter, r *http.Request) {
	var body kube.KusoPlacement
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	// Pass nil when both fields are empty so the server stores no
	// placement at all (and the helm chart skips the nodeSelector
	// block) instead of an empty struct that some clients might
	// surface as "is set, just empty."
	var p *kube.KusoPlacement
	if len(body.Labels) > 0 || len(body.Nodes) > 0 {
		p = &body
	}
	if err := h.Svc.SetPlacement(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"), p); err != nil {
		h.fail(w, "addon placement", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
