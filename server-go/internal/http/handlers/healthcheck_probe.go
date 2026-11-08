package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/projects"
)

// TestHealthcheck probes a (possibly unsaved) health-check path on one of
// the service's own ready pods. Gated at editor — the same role that can
// save the health-check settings — because it makes the server issue a
// request into the tenant workload.
func (h *ProjectsHandler) TestHealthcheck(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	var req projects.HealthProbeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	podSvc, podEnv := envGroupTarget(ctx, h.Kube, h.Namespace, project, service, req.Env)
	req.Env = podEnv
	out, err := h.Svc.TestHealthcheck(ctx, project, podSvc, req)
	if err != nil {
		h.fail(w, "probe environment", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
