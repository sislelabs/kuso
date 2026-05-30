// Project-level shared secrets handler. Routes:
//   GET    /api/projects/{project}/shared-secrets        → key list
//   PUT    /api/projects/{project}/shared-secrets        → upsert key
//   DELETE /api/projects/{project}/shared-secrets/{key}  → remove key
//
// The values are write-only; the GET endpoint returns keys only so
// secrets can't leak via screen-share / browser cache. The kuso-server
// pre-populates every new env's envFromSecrets to include
// "<project>-shared" so the keys are auto-mounted as env vars in
// every service container.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/projectsecrets"
	"kuso/server/internal/secrets"
)

type ProjectSecretsHandler struct {
	Svc    *projectsecrets.Service
	DB     *db.DB
	Logger *slog.Logger
}

func (h *ProjectSecretsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/shared-secrets", h.List)
	r.Put("/api/projects/{project}/shared-secrets", h.Set)
	r.Delete("/api/projects/{project}/shared-secrets/{key}", h.Unset)
}

func projectSecretsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *ProjectSecretsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectSecretsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	keys, err := h.Svc.ListKeys(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list shared secrets", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

type setSharedSecretBody struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	// Force=true bypasses the shadow check that would otherwise
	// refuse the write when a service-scoped Secret holds the same
	// key. CLI maps this to --force.
	Force bool `json:"force"`
}

func (h *ProjectSecretsHandler) Set(w http.ResponseWriter, r *http.Request) {
	var body setSharedSecretBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Key == "" {
		http.Error(w, "key required", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectSecretsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	rolled, err := h.Svc.SetKey(ctx, chi.URLParam(r, "project"), body.Key, body.Value, projectsecrets.SetOptions{Force: body.Force})
	if err != nil {
		h.fail(w, "set shared secret", err)
		return
	}
	// 200 with body so the CLI can surface the rollout count. Previous
	// 204-no-content gave the user no signal that anything happened
	// downstream of the Secret update — leading to the "set the value
	// but pods don't see it" trap that motivated this fix.
	writeJSON(w, http.StatusOK, map[string]any{"rolled": rolled})
}

func (h *ProjectSecretsHandler) Unset(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectSecretsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	rolled, err := h.Svc.UnsetKey(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "key"))
	if err != nil {
		h.fail(w, "unset shared secret", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rolled": rolled})
}

func (h *ProjectSecretsHandler) fail(w http.ResponseWriter, op string, err error) {
	if shadow := secrets.AsShadowed(err); shadow != nil {
		// 409 + structured body so the CLI can render a helpful
		// "X already set on service <svc> as service-scoped; unset
		// it or pass --force" message instead of just "conflict".
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":    err.Error(),
			"code":     "shadowed",
			"key":      shadow.Key,
			"scope":    shadow.Scope,
			"services": shadow.Services,
		})
		return
	}
	switch {
	case errors.Is(err, projectsecrets.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, projectsecrets.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	default:
		h.Logger.Error("project secrets handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
