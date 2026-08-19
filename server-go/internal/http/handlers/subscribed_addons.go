package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// GET /api/projects/{project}/services/{service}/subscribed-addons
func (h *ProjectsHandler) GetSubscribedAddons(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListSubscribableAddons(ctx, project, chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "list subscribable addons", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/projects/{project}/services/{service}/subscribed-addons
// Body: { "addons": ["pg", "cache", ...] }
func (h *ProjectsHandler) SetSubscribedAddons(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Addons []string `json:"addons"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if body.Addons == nil {
		body.Addons = []string{}
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	updated, err := h.Svc.SetSubscribedAddons(ctx, project, chi.URLParam(r, "service"), body.Addons)
	if err != nil {
		h.fail(w, "set subscribed addons", err)
		return
	}
	// SECURITY: same reason as shared_env_keys.go — the echoed
	// KusoService carries plaintext spec.envVars and a possibly
	// token-bearing repo URL. This route is EDITOR-gated, but reading
	// env VALUES needs secrets:read, so an editor could otherwise
	// harvest every secret on the service by toggling a subscription.
	maskServiceEnvIfNeeded(ctx, h.DB, project, updated)
	redactServiceRepoIfNeeded(ctx, h.DB, project, updated)
	writeJSON(w, http.StatusOK, updated)
}
