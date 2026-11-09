package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
)

// Branches backs the settings Branch picker: the service repo's branch
// names, read with the service's own clone credentials. Editor-gated like
// the branch field it feeds; viewers can't change the branch.
func (h *BuildsHandler) Branches(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if !requireParent(ctx, w, h.Svc.Kube, h.Svc.Namespace, project, service) {
		return
	}
	out, err := h.Svc.ListRepoBranches(ctx, project, service)
	if errors.Is(err, builds.ErrRepoUnreachable) {
		h.Logger.Info("branches: list", "project", project, "service", service, "err", err)
		writeErr(w, http.StatusBadGateway, "couldn't list the repository's branches; type the branch name instead")
		return
	}
	if err != nil {
		h.fail(w, "list branches", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
