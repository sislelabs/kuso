package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	apiv1 "github.com/sislelabs/kuso/api/apiv1"

	"kuso/server/internal/db"
	"kuso/server/internal/projects"
)

// PUT /api/projects/{project}/services/{service}/envs/{env}/domains
// Body: { "hosts": ["api-staging.example.com", ...] }
// Replaces the env's AdditionalHosts outright. Use for the dashboard
// Networking section's bulk save; for incremental add/remove the
// CLI uses POST/DELETE variants below.
func (h *ProjectsHandler) SetEnvDomains(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Hosts []string `json:"hosts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Hosts == nil {
		body.Hosts = []string{}
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	updated, err := h.Svc.SetEnvDomains(ctx, project, chi.URLParam(r, "service"), chi.URLParam(r, "env"), body.Hosts)
	if err != nil {
		h.fail(w, "set env domains", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// POST /api/projects/{project}/services/{service}/envs/{env}/domains
// Body: { "host": "api-staging.example.com" }
func (h *ProjectsHandler) AddEnvDomain(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	updated, err := h.Svc.AddEnvDomain(ctx, project, chi.URLParam(r, "service"), chi.URLParam(r, "env"), body.Host)
	if err != nil {
		h.fail(w, "add env domain", err)
		return
	}
	writeJSON(w, http.StatusCreated, updated)
}

// DELETE /api/projects/{project}/services/{service}/envs/{env}/domains/{host}
func (h *ProjectsHandler) RemoveEnvDomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	updated, err := h.Svc.RemoveEnvDomain(ctx, project, chi.URLParam(r, "service"), chi.URLParam(r, "env"), chi.URLParam(r, "host"))
	if err != nil {
		h.fail(w, "remove env domain", err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// PUT /api/projects/{project}/services/{service}/envs/{env}/env-vars/{name}
// Body: {value} XOR {secretRef:{name,key}}. Sets a per-env override that
// wins over the service-level value for the same key (see SetEnvScopedVar).
func (h *ProjectsHandler) SetEnvScopedVar(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.SetEnvVarRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	req := projects.SetEnvVarRequest{Value: wire.Value}
	if wire.SecretRef != nil {
		req.SecretRef = &projects.SetEnvVarSecretRefBody{Name: wire.SecretRef.Name, Key: wire.SecretRef.Key}
	}
	out, err := h.Svc.SetEnvScopedVar(ctx, project, chi.URLParam(r, "service"), chi.URLParam(r, "env"), chi.URLParam(r, "name"), req)
	if err != nil {
		h.fail(w, "set env-scoped var", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DELETE /api/projects/{project}/services/{service}/envs/{env}/env-vars/{name}
func (h *ProjectsHandler) UnsetEnvScopedVar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.UnsetEnvScopedVar(ctx, project, chi.URLParam(r, "service"), chi.URLParam(r, "env"), chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "unset env-scoped var", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
