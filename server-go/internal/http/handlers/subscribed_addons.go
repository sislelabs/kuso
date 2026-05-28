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
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Addons == nil {
		body.Addons = []string{}
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	updated, err := h.Svc.SetSubscribedAddons(ctx, project, chi.URLParam(r, "service"), body.Addons)
	if err != nil {
		h.fail(w, "set subscribed addons", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
