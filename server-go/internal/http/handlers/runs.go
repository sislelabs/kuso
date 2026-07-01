// HTTP surface for KusoRun (one-shot task pods). Endpoints:
//
//   GET    /api/projects/{p}/services/{s}/runs          list
//   POST   /api/projects/{p}/services/{s}/runs          create
//   GET    /api/projects/{p}/runs/{run}                 get
//   POST   /api/projects/{p}/runs/{run}/cancel          cancel
//   DELETE /api/projects/{p}/runs/{run}                 delete (terminal only)
//
// All routes require Deployer+ on the project (creation, cancel,
// delete) or Viewer+ (list, get). Mirrors the builds endpoints.

package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/runs"
)

type RunsHandler struct {
	Svc    *runs.Service
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

func (h *RunsHandler) Mount(r chi.Router) {
	if h.Svc == nil {
		return
	}
	r.Get("/api/projects/{project}/services/{service}/runs", h.List)
	r.Post("/api/projects/{project}/services/{service}/runs", h.Create)
	r.Get("/api/projects/{project}/runs/{run}", h.Get)
	r.Post("/api/projects/{project}/runs/{run}/cancel", h.Cancel)
	r.Delete("/api/projects/{project}/runs/{run}", h.Delete)
}

func runsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

func (h *RunsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := runsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.List(ctx, project, service)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *RunsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req runs.CreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	ctx, cancel := runsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	// Triggering a run executes an ARBITRARY command inside the prod pod
	// with the service's full env (DATABASE_URL, secrets) — equivalent to
	// a pod shell, so it's ADMIN-ONLY in role-system v2 (an editor could
	// otherwise `printenv` past the env-value + shell restrictions). The
	// read-only run endpoints (Get/List/logs) stay at viewer/editor.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanReadSecrets(ctx, h.DB, project) {
		http.Error(w, "forbidden: triggering a run requires the admin role", http.StatusForbidden)
		return
	}
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		req.TriggeredBy = "user"
		req.TriggeredByUser = claims.Username
	} else {
		req.TriggeredBy = "api"
	}
	out, err := h.Svc.Create(ctx, project, service, req)
	if err != nil {
		h.fail(w, "create", err)
		return
	}
	if h.Audit != nil {
		// Runs execute arbitrary commands inside the production
		// environment with the service's full env (DATABASE_URL,
		// API keys, signing secrets). Every fire is privileged and
		// belongs in the audit trail with the full argv so a future
		// "who ran `DELETE FROM users`" forensic walk has the
		// evidence inline rather than requiring kubectl-archaeology
		// of long-since-GC'd Job pods.
		cmd := strings.Join(req.Command, " ")
		if len(cmd) > 512 {
			cmd = cmd[:512] + "…"
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "run.create",
			Pipeline: project,
			App:      service,
			Resource: "kusorun",
			Message:  fmt.Sprintf("ran %q on %s/%s as %s", cmd, project, service, out.Name),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *RunsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := runsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.Get(ctx, project, chi.URLParam(r, "run"))
	if err != nil {
		h.fail(w, "get", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *RunsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := runsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	runName := chi.URLParam(r, "run")
	if err := h.Svc.Cancel(ctx, project, runName); err != nil {
		h.fail(w, "cancel", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "run.cancel",
			Pipeline: project,
			Resource: "kusorun",
			Message:  fmt.Sprintf("cancelled run %s in project %q", runName, project),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RunsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := runsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.Delete(ctx, project, chi.URLParam(r, "run")); err != nil {
		h.fail(w, "delete", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RunsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, runs.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, runs.ErrNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, runs.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		h.Logger.Error("runs handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
