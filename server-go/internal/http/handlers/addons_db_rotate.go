package handlers

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
)

// RotateDBPassword is POST .../addons/{a}/rotate-password. Project admin,
// like the s3 rotation: it changes the credential every subscribed service
// uses and restarts them.
func (h *AddonsHandler) RotateDBPassword(w http.ResponseWriter, r *http.Request) {
	project, name := chi.URLParam(r, "project"), chi.URLParam(r, "addon")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	if !h.requireAddonOwned(ctx, w, project, name) {
		return
	}
	res, err := h.Svc.RotateDBPassword(ctx, project, name)
	if err != nil {
		h.fail(w, "rotate addon password", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User: auditUser(ctx), Severity: "critical", Action: "addon.password_rotate",
			Pipeline: project, App: name, Resource: "kusoaddon",
			Message: fmt.Sprintf("password rotated for %s/%s user %s (root rotated: %v)", project, name, res.User, res.RootRotated),
		})
	}
	writeJSON(w, http.StatusOK, res)
}
