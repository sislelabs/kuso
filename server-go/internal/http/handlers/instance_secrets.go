// Instance-shared secrets handler. Routes:
//   GET    /api/instance-secrets        → key list (no values)
//   PUT    /api/instance-secrets        → upsert {key, value}
//   DELETE /api/instance-secrets/{key}  → remove key
//
// Admin-gated at the router level; the auto-attach into every
// env's envFromSecrets is server-side (no caller perms required
// for the auto-mount to take effect).

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/instancesecrets"
)

type InstanceSecretsHandler struct {
	Svc    *instancesecrets.Service
	Logger *slog.Logger
}

func (h *InstanceSecretsHandler) Mount(r chi.Router) {
	r.Get("/api/instance-secrets", h.List)
	r.Put("/api/instance-secrets", h.Set)
	r.Delete("/api/instance-secrets/{key}", h.Unset)
}

func instanceSecretsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *InstanceSecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := instanceSecretsCtx(r)
	defer cancel()
	keys, err := h.Svc.ListKeys(ctx)
	if err != nil {
		h.fail(w, "list instance secrets", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type setInstanceSecretBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func (h *InstanceSecretsHandler) Set(w http.ResponseWriter, r *http.Request) {
	var body setInstanceSecretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	ctx, cancel := instanceSecretsCtx(r)
	defer cancel()
	if err := h.Svc.SetKey(ctx, body.Key, body.Value); err != nil {
		h.fail(w, "set instance secret", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *InstanceSecretsHandler) Unset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := instanceSecretsCtx(r)
	defer cancel()
	if err := h.Svc.UnsetKey(ctx, chi.URLParam(r, "key")); err != nil {
		h.fail(w, "unset instance secret", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *InstanceSecretsHandler) fail(w http.ResponseWriter, op string, err error) {
	if errors.Is(err, instancesecrets.ErrInvalid) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.Logger.Error("instance secrets handler", "op", op, "err", err)
	http.Error(w, "internal", http.StatusInternalServerError)
}
