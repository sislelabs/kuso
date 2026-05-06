package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/spec"
)

// ProjectsHandler holds the projects.Service the routes call. The
// Kube/Namespace/Reconciler fields back the config-as-code endpoint
// (POST /api/projects/{p}/apply); they're optional and the handler
// returns 503 when nil.
type ProjectsHandler struct {
	Svc        *projects.Service
	Logger     *slog.Logger
	Kube       *kube.Client
	Namespace  string
	Reconciler *spec.Reconciler
	// DB is used for the tenancy filter on /api/projects (admins
	// bypass; everyone else sees only projects they belong to).
	// Optional: when nil the filter no-ops, preserving the
	// pre-tenancy "everyone sees everything" behaviour.
	DB *db.DB
}

// Mount registers all /api/projects/* routes onto the given router.
func (h *ProjectsHandler) Mount(r chi.Router) {
	r.Get("/api/projects", h.List)
	r.Post("/api/projects", h.Create)
	r.Get("/api/projects/{project}", h.Describe)
	r.Patch("/api/projects/{project}", h.Update)
	r.Delete("/api/projects/{project}", h.Delete)

	r.Get("/api/projects/{project}/services", h.ListServices)
	r.Post("/api/projects/{project}/services", h.AddService)
	r.Get("/api/projects/{project}/services/{service}", h.GetService)
	r.Patch("/api/projects/{project}/services/{service}", h.PatchService)
	r.Delete("/api/projects/{project}/services/{service}", h.DeleteService)
	// Delta operations on the most-edited fields. PatchService takes a
	// whole-list replacement which last-write-wins under concurrent
	// edits; these endpoints serialise per (project, service) so two
	// simultaneous "add this domain" / "set this env var" calls both
	// land. See server-go/internal/projects/services_deltas.go.
	r.Post("/api/projects/{project}/services/{service}/domains", h.AddDomain)
	r.Delete("/api/projects/{project}/services/{service}/domains/{host}", h.RemoveDomain)
	r.Put("/api/projects/{project}/services/{service}/env-vars/{name}", h.SetEnvVar)
	r.Delete("/api/projects/{project}/services/{service}/env-vars/{name}", h.UnsetEnvVar)
	// Rename is a separate endpoint because it's clone-then-delete
	// rather than a normal patch — the URL the new resource lives
	// at is different from the one the request came in on, and
	// callers need to know the cost (brief downtime + DNS cutover).
	r.Post("/api/projects/{project}/services/{service}/rename", h.RenameService)
	// Config-as-code: plan/apply a kuso.yml against the project. Body
	// is the raw YAML; ?dryRun=1 returns the plan without writing.
	r.Post("/api/projects/{project}/apply", h.Apply)
	r.Get("/api/projects/{project}/services/{service}/env", h.GetEnv)
	r.Post("/api/projects/{project}/services/{service}/env", h.SetEnv)
	// Env-var detection from the most recent build's source-scan
	// (env-detect init container). Returns the names + the timestamp
	// of the build that produced them — UI flags any name that's
	// referenced in source but missing from the saved env.
	r.Get("/api/projects/{project}/services/{service}/env/detected", h.GetDetectedEnv)
	// Custom environments: POST .../envs creates a non-prod, non-preview
	// env (e.g. staging on a branch). Production auto-creates with the
	// service; preview envs come from the GH PR webhook.
	r.Post("/api/projects/{project}/services/{service}/envs", h.AddEnvironment)
	r.Post("/api/projects/{project}/services/{service}/wake", h.Wake)
	// Pods lookup for a service+env. Used by `kuso shell` to resolve
	// a target pod for kubectl exec, and by future shell tab in the
	// web UI. Slim summary — name, ready, container list.
	r.Get("/api/projects/{project}/services/{service}/pods", h.ListPods)

	r.Get("/api/projects/{project}/envs", h.ListEnvironments)
	r.Get("/api/projects/{project}/envs/{env}", h.GetEnvironment)
	r.Delete("/api/projects/{project}/envs/{env}", h.DeleteEnvironment)
}

// projectCtx pulls a 5-second timeout context from the request. Same
// budget as the auth handler — kube round-trips against the live cluster
// can occasionally stall and the caller is on a synchronous HTTP request.
func projectCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "list projects", err)
		return
	}
	// Tenancy filter: non-admins only see projects they have a
	// ProjectMembership on. Admins (settings:admin) bypass with the
	// full list. Pending users get an empty array — they're auth'd
	// but invisible to the rest of the system.
	if claims, ok := auth.ClaimsFromContext(ctx); ok && !auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		if h.DB != nil {
			tenancy, terr := h.DB.ListUserTenancy(ctx, claims.UserID)
			if terr == nil {
				allowed := map[string]struct{}{}
				for _, m := range tenancy.ProjectMemberships {
					allowed[m.Project] = struct{}{}
				}
				filtered := out[:0]
				for _, p := range out {
					if _, ok := allowed[p.Name]; ok {
						filtered = append(filtered, p)
					}
				}
				out = filtered
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Project creation is an instance-level action — only admins (or
	// users with project:write). Non-admins can't conjure new projects.
	if !requirePerm(w, r, auth.PermProjectWrite) {
		return
	}
	var req projects.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Create(ctx, req)
	if err != nil {
		h.fail(w, "create project", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) Describe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.Describe(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "describe project", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Update is PATCH /api/projects/{project}. Body is a partial spec —
// see projects.UpdateProjectRequest. Pointer fields distinguish unset
// from set-to-zero so callers can explicitly toggle previews.enabled.
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var req projects.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleOwner) {
		return
	}
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), req)
	if err != nil {
		h.fail(w, "update project", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleOwner) {
		return
	}
	if err := h.Svc.Delete(ctx, chi.URLParam(r, "project")); err != nil {
		h.fail(w, "delete project", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ProjectsHandler) ListServices(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListServices(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list services", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) AddService(w http.ResponseWriter, r *http.Request) {
	var req projects.CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.AddService(ctx, chi.URLParam(r, "project"), req)
	if err != nil {
		h.fail(w, "add service", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) GetService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get service", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Apply ingests a kuso.yml body (POST /api/projects/{p}/apply), diffs
// it against the live project, and applies the resulting plan. With
// ?dryRun=1 we just return the plan without touching kube. The
// project URL param must match the YAML's `project:` field — we
// refuse cross-project applies so an accidental wrong-repo push
// can't wipe out another project.
func (h *ProjectsHandler) Apply(w http.ResponseWriter, r *http.Request) {
	if h.Reconciler == nil {
		http.Error(w, "config-as-code disabled (kube unavailable)", http.StatusServiceUnavailable)
		return
	}
	body := make([]byte, 0, 1<<14)
	buf := make([]byte, 4096)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
		if len(body) > 1<<20 {
			http.Error(w, "kuso.yml too large (>1MiB)", http.StatusRequestEntityTooLarge)
			return
		}
	}
	f, err := spec.Parse(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if f.Project != chi.URLParam(r, "project") {
		http.Error(w, "project name in YAML doesn't match URL", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}

	plan, err := spec.PlanFor(ctx, h.Kube, h.Namespace, f)
	if err != nil {
		h.Logger.Error("apply: plan", "err", err)
		http.Error(w, "plan failed", http.StatusInternalServerError)
		return
	}
	if r.URL.Query().Get("dryRun") == "1" {
		writeJSON(w, http.StatusOK, plan)
		return
	}
	res, err := h.Reconciler.Apply(ctx, plan, f)
	if err != nil {
		h.Logger.Error("apply: execute", "err", err)
		http.Error(w, "apply failed", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("apply", "project", f.Project, "plan", plan.Summary(), "errs", len(res.Errors))
	writeJSON(w, http.StatusOK, res)
}

// PatchService accepts a partial KusoService.spec update. Body shape
// matches projects.PatchServiceRequest — every field is optional.
func (h *ProjectsHandler) PatchService(w http.ResponseWriter, r *http.Request) {
	var req projects.PatchServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.PatchService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req)
	if err != nil {
		h.fail(w, "patch service", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AddDomain appends a single domain to a service's spec.domains. Body
// is projects.AddDomainRequest. The mutation is per-service serialised
// so two concurrent adds don't race.
func (h *ProjectsHandler) AddDomain(w http.ResponseWriter, r *http.Request) {
	var req projects.AddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.AddDomain(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req)
	if err != nil {
		h.fail(w, "add domain", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// RemoveDomain drops a single host from spec.domains. ErrNotFound on
// an unknown host so an idempotent retry surfaces clearly.
func (h *ProjectsHandler) RemoveDomain(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.RemoveDomain(ctx,
		chi.URLParam(r, "project"),
		chi.URLParam(r, "service"),
		chi.URLParam(r, "host"))
	if err != nil {
		h.fail(w, "remove domain", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// SetEnvVar adds or overwrites a single env var by name. Body is
// projects.SetEnvVarRequest — exactly one of `value` / `secretRef`.
func (h *ProjectsHandler) SetEnvVar(w http.ResponseWriter, r *http.Request) {
	var req projects.SetEnvVarRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	name := chi.URLParam(r, "name")
	out, err := h.Svc.SetEnvVar(ctx, project, service, name, req)
	if err != nil {
		h.fail(w, "set env var", err)
		return
	}
	// Clear any pending crash hint for this var: the user just set
	// it, so the "your last crash mentioned X" pip should disappear
	// without waiting for the next crash to confirm. Best-effort.
	_ = h.DB.DeleteEnvHint(ctx, project, service, name)
	writeJSON(w, http.StatusOK, out)
}

// UnsetEnvVar removes a single env var by name. ErrNotFound on
// unknown name.
func (h *ProjectsHandler) UnsetEnvVar(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.UnsetEnvVar(ctx,
		chi.URLParam(r, "project"),
		chi.URLParam(r, "service"),
		chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "unset env var", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) DeleteService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	if err := h.Svc.DeleteService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service")); err != nil {
		h.fail(w, "delete service", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RenameService takes a {newName} body and clones the service +
// envs under the new name, then deletes the old. Returns the new
// service spec on success so the client can update its URL state.
func (h *ProjectsHandler) RenameService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NewName string `json:"newName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.NewName == "" {
		http.Error(w, "newName required", http.StatusBadRequest)
		return
	}
	// Rename can take a few seconds (helm-operator reconciles two
	// helm releases — the new one + the old uninstall) so we give
	// it a longer budget than projectCtx default.
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.RenameService(ctx,
		chi.URLParam(r, "project"),
		chi.URLParam(r, "service"),
		req.NewName,
	)
	if err != nil {
		h.fail(w, "rename service", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) GetEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	// Env vars are a Deployer concern (they affect runtime behaviour).
	// Viewers shouldn't see secrets even when fully resolved.
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.GetEnv(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"envVars": out})
}

// GetDetectedEnv returns env-var names kuso detected as referenced
// but possibly missing. Two sources, merged into one response:
//
//   1. Build-time scan: names surfaced from .env.example + source
//      grep by the env-detect init container on the most recent
//      build. High signal but stale until the next build.
//   2. Runtime crash hints: names extracted from the log shipper's
//      regex match on common "missing env" error messages
//      (KeyError, panic: missing X env, etc). Real-time.
//
// UI flags any name (from either source) that isn't in the saved
// env list, with a "Add" button to seed it. Returns:
//   { names: ["DATABASE_URL", ...], detectedAt: "2026-...",
//     hints: [{name, lastLine, lastSeen}, ...] }
func (h *ProjectsHandler) GetDetectedEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleDeployer) {
		return
	}
	names, ts, err := h.Svc.GetDetectedEnv(ctx, project, service)
	if err != nil {
		h.fail(w, "get detected env", err)
		return
	}
	hints, _ := h.DB.ListEnvHints(ctx, project, service)
	writeJSON(w, http.StatusOK, map[string]any{
		"names":      names,
		"detectedAt": ts,
		"hints":      hints,
	})
}

func (h *ProjectsHandler) SetEnv(w http.ResponseWriter, r *http.Request) {
	var req projects.SetEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	if err := h.Svc.SetEnv(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req.EnvVars); err != nil {
		h.fail(w, "set env", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Wake is POST /api/projects/{project}/services/{service}/wake. It
// nudges the production env's replica count back up so a sleeping
// service comes back online on the next reconcile tick.
func (h *ProjectsHandler) ListPods(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	env := r.URL.Query().Get("env")
	out, err := h.Svc.ListPods(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), env)
	if err != nil {
		h.fail(w, "list pods", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Wake(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	if err := h.Svc.WakeService(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service")); err != nil {
		h.fail(w, "wake service", err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *ProjectsHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListEnvironments(ctx, chi.URLParam(r, "project"))
	if err != nil {
		h.fail(w, "list envs", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// AddEnvironment creates a custom env (e.g. staging on a branch).
// Production envs auto-create with the service; preview envs come
// from the GH PR webhook; this is the "third kind" — long-lived,
// branch-bound, with its own URL.
func (h *ProjectsHandler) AddEnvironment(w http.ResponseWriter, r *http.Request) {
	var req projects.CreateEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.AddEnvironment(ctx,
		chi.URLParam(r, "project"),
		chi.URLParam(r, "service"),
		req,
	)
	if err != nil {
		h.fail(w, "add environment", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetEnvironment(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "env"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) DeleteEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	if err := h.Svc.DeleteEnvironment(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "env")); err != nil {
		h.fail(w, "delete env", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail maps domain errors to HTTP status codes. Anything we don't
// recognise is logged and returned as 500.
func (h *ProjectsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, projects.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, projects.ErrConflict):
		http.Error(w, "conflict", http.StatusConflict)
	case errors.Is(err, projects.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, projects.ErrCompositeVarRef):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		h.Logger.Error("projects handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

// writeJSON encodes v as JSON with the given status. Encoding errors are
// logged but not bubbled, since the response headers are already sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
