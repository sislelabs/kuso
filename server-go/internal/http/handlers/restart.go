package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
)

// restartRequest is the restart body. env is the env-group label
// ("production", "staging", "preview-pr-7"); empty means production.
type restartRequest struct {
	Env string `json:"env,omitempty"`
}

// Restart rolls one env's pods on the image they already run, without a
// build. POST /api/projects/{project}/services/{service}/restart → 202
// {"restartedAt": RFC3339}.
func (h *BuildsHandler) Restart(w http.ResponseWriter, r *http.Request) {
	var req restartRequest
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
			return
		}
	}
	if req.Env == "" {
		req.Env = "production"
	}
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	at, env, err := h.Svc.Restart(ctx, project, service, req.Env)
	if err != nil {
		h.fail(w, "restart service", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "service.restart",
			Pipeline: project,
			Phase:    req.Env,
			App:      service,
			Resource: "service",
			Message:  fmt.Sprintf("restarted %s (deployment %s) without a build", req.Env, env.Name),
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"restartedAt": at.Format(time.RFC3339)})
}

// RetryRelease re-runs the release hook of a release-failed build and
// promotes its image on success. POST .../builds/{build}/retry-release
// → 202 {"job": "<release job name>"}. The retry runs on the leader's
// build poller; poll the build list for the outcome.
func (h *BuildsHandler) RetryRelease(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service, build := chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "build")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	job, err := h.Svc.RetryRelease(ctx, project, service, build)
	if err != nil {
		h.fail(w, "retry release", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "build.retry_release",
			Pipeline: project,
			App:      service,
			Resource: "build",
			Message:  fmt.Sprintf("retrying release hook of build %s (job %s)", build, job),
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"job": job})
}
