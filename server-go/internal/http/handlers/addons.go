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
	// Edit version / size / HA / storageSize / database.
	r.Patch("/api/projects/{project}/addons/{addon}", h.Update)
	// Pin the addon's StatefulSet to a subset of nodes. PUT replaces
	// the placement struct verbatim; pass empty {} to clear.
	r.Put("/api/projects/{project}/addons/{addon}/placement", h.Placement)
	r.Get("/api/projects/{project}/addons/{addon}/secret-keys", h.SecretKeys)
	// Plaintext connection values. Gated behind secrets:read at the
	// router level so the autocomplete (keys-only) endpoint above
	// stays open to anyone with addons:read.
	r.Get("/api/projects/{project}/addons/{addon}/secret", h.Secret)
	// Re-mirror the source Secret into the addon's <name>-conn for
	// external addons. Useful after the upstream credentials rotated.
	r.Post("/api/projects/{project}/addons/{addon}/resync-external", h.ResyncExternal)
	// Re-provision the per-project DB on a shared instance addon
	// (Model 2). Rotates the password and refreshes <name>-conn.
	r.Post("/api/projects/{project}/addons/{addon}/resync-instance", h.ResyncInstance)
	// Recover from the helm-chart password drift bug: ALTER USER
	// inside the running postgres pod to match the conn secret.
	r.Post("/api/projects/{project}/addons/{addon}/repair-password", h.RepairPassword)
}

// RepairPassword resyncs the running postgres user's password to
// match the conn secret. Use after the chart's password-reuse
// lookup raced and generated a fresh random while pgdata was
// locked to the old one.
func (h *AddonsHandler) RepairPassword(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if err := h.Svc.RepairPassword(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "repair addon password", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResyncInstance re-provisions the per-project DB on a shared
// instance addon and rotates the password.
func (h *AddonsHandler) ResyncInstance(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if err := h.Svc.ResyncInstanceAddon(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "resync instance addon", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ResyncExternal triggers a re-mirror of the user-provided Secret
// for an external addon. 404 if not external.
func (h *AddonsHandler) ResyncExternal(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := addonsCtx(r)
	defer cancel()
	if err := h.Svc.ResyncExternal(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); err != nil {
		h.fail(w, "resync external addon", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// Update applies a partial update to the addon spec. Body shape is
// addons.UpdateAddonRequest — pointer fields, nil means "leave
// alone". Returns the updated CR so the UI can re-baseline.
func (h *AddonsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var body addons.UpdateAddonRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := addonsCtx(r)
	defer cancel()
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"), body)
	if err != nil {
		h.fail(w, "update addon", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
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
		// Pass the wrapped error through so the UI can show
		// "addon kuso-hello-go/postgres already exists" instead of
		// a bare "409 Conflict" toast.
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, addons.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("addons handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
