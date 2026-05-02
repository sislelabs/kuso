package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/secrets"
)

// SecretsHandler exposes per-service secret routes.
type SecretsHandler struct {
	Svc    *secrets.Service
	Logger *slog.Logger
}

// Mount registers the secret routes onto the given chi router.
func (h *SecretsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/secrets", h.List)
	r.Post("/api/projects/{project}/services/{service}/secrets", h.Set)
	r.Delete("/api/projects/{project}/services/{service}/secrets/{key}", h.Unset)
}

func secretsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// List returns the keys of the scoped Secret. Values are NEVER returned —
// only key listings, matching the TS contract.
func (h *SecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("env")
	ctx, cancel := secretsCtx(r)
	defer cancel()
	keys, err := h.Svc.ListKeys(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env)
	if err != nil {
		h.fail(w, "list secrets", err)
		return
	}
	if keys == nil {
		keys = []string{}
	}
	envOut := any(env)
	if env == "" {
		envOut = nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys, "env": envOut})
}

// setSecretRequest is the body of POST .../secrets.
type setSecretRequest struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Env   string `json:"env,omitempty"`
}

// Set upserts a single (key, value) into the scoped Secret. The key is
// required; value may be empty (legitimate use case: "I want this key
// present but blank").
func (h *SecretsHandler) Set(w http.ResponseWriter, r *http.Request) {
	var req setSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := secretsCtx(r)
	defer cancel()
	if err := h.Svc.SetKey(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req.Env, req.Key, req.Value); err != nil {
		h.fail(w, "set secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Unset removes a single key from the scoped Secret. Returns 404 when
// the key wasn't present.
func (h *SecretsHandler) Unset(w http.ResponseWriter, r *http.Request) {
	env := r.URL.Query().Get("env")
	ctx, cancel := secretsCtx(r)
	defer cancel()
	if err := h.Svc.UnsetKey(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env, chi.URLParam(r, "key")); err != nil {
		h.fail(w, "unset secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *SecretsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, secrets.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("secrets handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
