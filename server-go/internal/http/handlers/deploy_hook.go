package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/builds"
	"kuso/server/internal/db"
)

// maxHookBody bounds a webhook payload read; GitHub caps its own at 25 MB.
const maxHookBody = 25 << 20

// hookBuilds is the slice of builds.Service the public trigger needs.
type hookBuilds interface {
	VerifyDeployHook(ctx context.Context, project, service, presented string) bool
	DeployedBranches(ctx context.Context, project, service string) ([]string, error)
	CreateWithOutcome(ctx context.Context, project, service string, req builds.CreateBuildRequest) (builds.CreateOutcome, error)
}

// DeployHookHandler serves per-service deploy hooks: a secret URL that
// starts a build, for repos no GitHub App installation covers.
type DeployHookHandler struct {
	Hooks hookBuilds
	// Svc backs the authenticated management routes (nil in trigger-only tests).
	Svc    *builds.Service
	DB     *db.DB
	Audit  *audit.Service
	Logger *slog.Logger
}

// Mount registers the authenticated management routes.
func (h *DeployHookHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/deploy-hook", h.Get)
	r.Post("/api/projects/{project}/services/{service}/deploy-hook", h.Enable)
	r.Delete("/api/projects/{project}/services/{service}/deploy-hook", h.Disable)
}

// MountPublic registers the trigger. No JWT: the token in the path is the
// credential, so a repo webhook or a CI job can call it.
func (h *DeployHookHandler) MountPublic(r chi.Router) {
	r.Post("/api/hooks/deploy/{project}/{service}/{token}", h.Trigger)
}

type deployHookBody struct {
	Enabled bool `json:"enabled"`
	// URL is the full trigger URL. Only present for callers who may
	// trigger builds anyway (project editors).
	URL string `json:"url,omitempty"`
}

func deployHookURL(r *http.Request, project, service, token string) string {
	return fmt.Sprintf("%s/api/hooks/deploy/%s/%s/%s", publicBaseURL(r), project, service, token)
}

func (h *DeployHookHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	tok, err := h.Svc.DeployHookToken(ctx, project, service)
	if err != nil {
		h.fail(w, "get deploy hook", err)
		return
	}
	out := deployHookBody{Enabled: tok != ""}
	if tok != "" {
		out.URL = deployHookURL(r, project, service, tok)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *DeployHookHandler) Enable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Rotate bool `json:"rotate"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
			return
		}
	}
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	tok, err := h.Svc.EnsureDeployHook(ctx, project, service, req.Rotate)
	if err != nil {
		h.fail(w, "enable deploy hook", err)
		return
	}
	action := "deployhook.enable"
	if req.Rotate {
		action = "deployhook.rotate"
	}
	h.audit(ctx, action, project, service)
	writeJSON(w, http.StatusOK, deployHookBody{Enabled: true, URL: deployHookURL(r, project, service, tok)})
}

func (h *DeployHookHandler) Disable(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.DeleteDeployHook(ctx, project, service); err != nil {
		h.fail(w, "disable deploy hook", err)
		return
	}
	h.audit(ctx, "deployhook.disable", project, service)
	writeJSON(w, http.StatusOK, deployHookBody{Enabled: false})
}

func (h *DeployHookHandler) audit(ctx context.Context, action, project, service string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "warn",
		Action:   action,
		Pipeline: project,
		App:      service,
		Resource: "deploy-hook",
		Message:  action + " for " + project + "/" + service,
	})
}

// Trigger starts a build. Three callers, told apart by headers:
//   - a GitHub repo webhook (X-GitHub-Event): builds pushes to a branch one
//     of the service's environments deploys, at the pushed commit; every
//     other event is acknowledged and skipped;
//   - a GitLab push hook (X-Gitlab-Event): same;
//   - anything else (curl, CI): builds the default branch, or ?branch= /
//     ?ref=<40-char sha>.
func (h *DeployHookHandler) Trigger(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	// 404, not 401/403: don't confirm which services exist or have a hook.
	if !h.Hooks.VerifyDeployHook(ctx, project, service, chi.URLParam(r, "token")) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	req := builds.CreateBuildRequest{TriggeredBy: "api"}
	if event := pushEventName(r); event != "" {
		branch, sha, skip := parsePushHook(event, io.LimitReader(r.Body, maxHookBody))
		if skip == "" {
			deployed, err := h.Hooks.DeployedBranches(ctx, project, service)
			if err != nil {
				h.fail(w, "deploy hook branches", err)
				return
			}
			if !containsString(deployed, branch) {
				skip = fmt.Sprintf("no environment of %s/%s deploys branch %q", project, service, branch)
			}
		}
		if skip != "" {
			// 200: a non-2xx makes the git host flag the webhook as failing.
			writeJSON(w, http.StatusOK, map[string]string{"skipped": skip})
			return
		}
		// "webhook" keeps the commit-keyed build name, so a redelivered
		// push is deduplicated instead of rebuilt.
		req = builds.CreateBuildRequest{Branch: branch, Ref: sha, TriggeredBy: "webhook"}
	} else {
		req.Branch = strings.TrimSpace(r.URL.Query().Get("branch"))
		req.Ref = strings.TrimSpace(r.URL.Query().Get("ref"))
	}

	out, err := h.Hooks.CreateWithOutcome(ctx, project, service, req)
	if err != nil {
		h.fail(w, "deploy hook build", err)
		return
	}
	h.Logger.Info("deploy hook: build started", "project", project, "service", service, "branch", req.Branch, "ref", req.Ref, "existing", out.Existing)
	status, body := createBuildResponse(out)
	writeJSON(w, status, body)
}

// pushEventName returns the git host's webhook event header, "" for a
// plain call.
func pushEventName(r *http.Request) string {
	if e := r.Header.Get("X-GitHub-Event"); e != "" {
		return e
	}
	return r.Header.Get("X-Gitlab-Event")
}

// parsePushHook reads a GitHub or GitLab push payload. skip is non-empty
// when the event must not build, and says why.
func parsePushHook(event string, body io.Reader) (branch, sha, skip string) {
	if event != "push" && event != "Push Hook" {
		return "", "", "event " + event + " ignored (only pushes build)"
	}
	var p struct {
		Ref     string `json:"ref"`
		After   string `json:"after"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.NewDecoder(body).Decode(&p); err != nil {
		return "", "", "push payload is not JSON (set the webhook content type to application/json)"
	}
	branch, ok := strings.CutPrefix(p.Ref, "refs/heads/")
	if !ok || branch == "" {
		return "", "", "ref " + p.Ref + " is not a branch"
	}
	if p.Deleted || strings.Trim(p.After, "0") == "" {
		return "", "", "branch " + branch + " was deleted"
	}
	return branch, p.After, ""
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

func (h *DeployHookHandler) fail(w http.ResponseWriter, op string, err error) {
	(&BuildsHandler{Logger: h.Logger}).fail(w, op, err)
}
