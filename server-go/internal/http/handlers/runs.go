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
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
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
	maskRunEnvsIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusOK, out)
}

// maskRunEnvIfNeeded blanks literal env values on a KusoRun before it
// goes out to a caller without secrets:read. Mirrors maskEnvIfNeeded.
//
// A run's spec.env is a RESOLVED SNAPSHOT of the production environment's
// env vars, taken at create time by runs.mergeRunEnv — the same data
// maskEnvIfNeeded withholds on /envs. Creating a run is deliberately
// admin-only ("equivalent to a pod shell … an editor could otherwise
// printenv past the env-value restriction"), but the read side handed the
// same printenv output back at VIEWER level, no shell required, and the
// CR persists until GC.
func maskRunEnvIfNeeded(ctx context.Context, dbConn *db.DB, project string, run *kube.KusoRun) {
	if run == nil || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	maskKusoRunEnv(run.Spec.Env)
}

// maskRunEnvsIfNeeded is the slice form for list endpoints.
func maskRunEnvsIfNeeded(ctx context.Context, dbConn *db.DB, project string, runs []kube.KusoRun) {
	if len(runs) == 0 || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	for i := range runs {
		maskKusoRunEnv(runs[i].Spec.Env)
	}
}

// runCredentialRE matches credentials commonly typed inline on a run
// command line: a URL userinfo section (postgres://user:pw@host) and
// --flag=value / KEY=value pairs whose key looks secret-ish.
var runCredentialRE = regexp.MustCompile(
	`(?i)(//[^/\s:]+:)[^@\s]+(@)` +
		`|((?:^|\s)(?:--)?[\w.-]*(?:passw(?:or)?d|secret|token|api[_-]?key|access[_-]?key|auth)[\w.-]*[=:\s])\S+`)

// redactRunCommand strips inline credentials from a run's argv before it
// is written to the audit log.
//
// Creating a run is admin-only precisely because it is equivalent to a
// pod shell. But the audit row it writes is tagged Pipeline: project and
// GET /api/audit?project=<p> serves project-scoped rows to any VIEWER —
// so `kuso run -- psql "postgres://app:hunter2@db/app"` handed that
// password to every viewer. Same privilege inversion the ssh-key and
// instance-secret audit paths already guard against (see
// audit_redaction_test.go).
//
// The command itself is deliberately still recorded: "who ran DELETE FROM
// users" is the whole point of auditing runs. Only the credential-shaped
// parts are replaced.
func redactRunCommand(cmd string) string {
	return runCredentialRE.ReplaceAllString(cmd, "${1}${3}"+envMaskSentinel+"${2}")
}

// maskKusoRunEnv is the KusoRunEnv counterpart of maskKusoEnvVars.
// valueFrom entries are left alone: they are secret REFERENCES, not
// values, and the same is true on the env path.
func maskKusoRunEnv(vars []kube.KusoRunEnv) {
	for i := range vars {
		if vars[i].Value != "" {
			vars[i].Value = envMaskSentinel
		}
	}
}

func (h *RunsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req runs.CreateRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
		writeErr(w, http.StatusForbidden, "forbidden: triggering a run requires the admin role")
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
		cmd := redactRunCommand(strings.Join(req.Command, " "))
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
	maskRunEnvIfNeeded(ctx, h.DB, project, out)
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
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, runs.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, runs.ErrNotFound, "run"))
	case errors.Is(err, runs.ErrConflict):
		writeErr(w, http.StatusConflict, err.Error())
	default:
		h.Logger.Error("runs handler", "op", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}
