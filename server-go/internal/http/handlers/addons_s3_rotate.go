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

// RotateS3Credentials is POST .../addons/{a}/s3/rotate-credentials. Project
// admin: it changes the credential every subscribed service uses.
func (h *AddonsHandler) RotateS3Credentials(w http.ResponseWriter, r *http.Request) {
	project, name := chi.URLParam(r, "project"), chi.URLParam(r, "addon")
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	if !h.requireAddonOwned(ctx, w, project, name) {
		return
	}
	res, err := h.Svc.RotateS3Credentials(ctx, project, name)
	if err != nil {
		h.fail(w, "rotate s3 credentials", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User: auditUser(ctx), Severity: "critical", Action: "addon.s3_rotate",
			Pipeline: project, App: name, Resource: "kusoaddon",
			Message: fmt.Sprintf("s3 credentials rotated for %s/%s (new key %s, previous was root: %v)", project, name, res.AccessKeyID, res.PreviousWasRoot),
		})
	}
	writeJSON(w, http.StatusOK, res)
}

// RevokePreviousS3Credentials is POST .../addons/{a}/s3/revoke-previous.
func (h *AddonsHandler) RevokePreviousS3Credentials(w http.ResponseWriter, r *http.Request) {
	project, name := chi.URLParam(r, "project"), chi.URLParam(r, "addon")
	ctx, cancel := context.WithTimeout(r.Context(), 4*time.Minute)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleAdmin) {
		return
	}
	if !h.requireAddonOwned(ctx, w, project, name) {
		return
	}
	key, err := h.Svc.RevokePreviousS3Credentials(ctx, project, name)
	if err != nil {
		h.fail(w, "revoke previous s3 credentials", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User: auditUser(ctx), Severity: "critical", Action: "addon.s3_revoke",
			Pipeline: project, App: name, Resource: "kusoaddon",
			Message: fmt.Sprintf("previous s3 key %s removed from %s/%s", key, project, name),
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"revokedAccessKeyId": key})
}
