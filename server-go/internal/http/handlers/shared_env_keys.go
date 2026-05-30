package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// GET /api/projects/{project}/services/{service}/shared-env-keys
// Returns the available keys (grouped by source secret) plus the
// service's current subscription list. Dashboard renders the chip
// toggle from this.
func (h *ProjectsHandler) GetSharedEnvKeys(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListSubscribableSharedKeys(ctx, project, chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "list subscribable shared keys", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PUT /api/projects/{project}/services/{service}/shared-env-keys
// Body: { "keys": ["DATABASE_URL", "JWT_SECRET", ...] }
// Replaces the subscription list outright. Pass [] (non-nil empty)
// to subscribe to nothing; the dashboard's "Add all" / "Clear all"
// buttons fan out to this same endpoint.
func (h *ProjectsHandler) SetSharedEnvKeys(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Keys []string `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// json.Decode leaves Keys == nil when the field is missing, which
	// the service layer rejects (nil means "stay in legacy mode" and
	// has to be requested explicitly via a separate verb). Coerce nil
	// to [] so a caller-side typo doesn't accidentally revert the
	// subscription.
	if body.Keys == nil {
		body.Keys = []string{}
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	updated, err := h.Svc.SetSharedEnvKeys(ctx, project, chi.URLParam(r, "service"), body.Keys)
	if err != nil {
		h.fail(w, "set sharedEnvKeys", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}
