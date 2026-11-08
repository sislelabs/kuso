package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/builds"
	"kuso/server/internal/db"
)

// EnvExample returns the keys + example values of the service repo's
// .env.example (or .env.sample) for the Variables import dialog. Editors
// only, like writing env vars: example files in private repos can hold
// real values, and viewers can't import anyway. A repo without the file is a 200 with an
// empty file and the paths checked, so a 404 only ever means "service
// not found" or "this server predates the endpoint".
func (h *BuildsHandler) EnvExample(w http.ResponseWriter, r *http.Request) {
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
	out, err := h.Svc.ReadEnvExample(ctx, project, service)
	if errors.Is(err, builds.ErrRepoUnreachable) {
		h.Logger.Info("env example: read repo", "project", project, "service", service, "err", err)
		writeErr(w, http.StatusBadGateway, "couldn't read the repository from its git host; try again in a minute")
		return
	}
	if err != nil {
		h.fail(w, "env example", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
