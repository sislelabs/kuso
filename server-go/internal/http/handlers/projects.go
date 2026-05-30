package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	apiv1 "github.com/sislelabs/kuso/api/apiv1"
	"gopkg.in/yaml.v3"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
	"kuso/server/internal/spec"
)

// apiv1CreateToDomain narrows the shared wire DTO down to the
// internal projects.CreateProjectRequest shape. Keeping the wire
// type in apiv1 lets the CLI + future clients share JSON tags
// (the source of truth for the API contract) without forcing every
// internal service to import the apiv1 module.
//
// When a field exists on both sides we copy it. When only one side
// has it (today: nothing — they're in sync), this function is where
// the divergence gets reconciled. If apiv1 ever adds a field the
// domain doesn't carry, this is the discard point.
func apiv1CreateToDomain(in apiv1.CreateProjectRequest) projects.CreateProjectRequest {
	out := projects.CreateProjectRequest{
		Name:        in.Name,
		Description: in.Description,
		BaseDomain:  in.BaseDomain,
		Namespace:   in.Namespace,
	}
	if in.DefaultRepo != nil {
		out.DefaultRepo = &projects.CreateProjectRepoSpec{
			URL:           in.DefaultRepo.URL,
			DefaultBranch: in.DefaultRepo.DefaultBranch,
		}
	}
	if in.GitHub != nil {
		out.GitHub = &projects.CreateProjectGithubSpec{InstallationID: in.GitHub.InstallationID}
	}
	if in.Previews != nil {
		out.Previews = &projects.CreateProjectPreviewsSpec{
			Enabled:    in.Previews.Enabled,
			TTLDays:    in.Previews.TTLDays,
			BaseDomain: in.Previews.BaseDomain,
		}
	}
	return out
}

// apiv1CreateServiceToDomain converts the wire shape for POST
// /api/projects/{p}/services to the internal request. apiv1 owns
// the JSON contract; the domain struct is purely internal now.
func apiv1CreateServiceToDomain(in apiv1.CreateServiceRequest) projects.CreateServiceRequest {
	out := projects.CreateServiceRequest{
		Name:        in.Name,
		DisplayName: in.DisplayName,
		Runtime:     in.Runtime,
		Command:     in.Command,
		Port:        in.Port,
		// FromService is required for runtime=worker (sibling service
		// whose image the worker reuses). Pre-fix this field was
		// silently dropped during apiv1 → domain conversion, so every
		// `kuso project service add ... --runtime worker --from-service
		// X` request hit the server's "fromService required" check
		// because the field never crossed the wire. Worker creation
		// has been broken via the API since the apiv1 split.
		FromService: in.FromService,
	}
	if in.Repo != nil {
		out.Repo = &projects.CreateServiceRepo{URL: in.Repo.URL, Path: in.Repo.Path}
	}
	if len(in.Domains) > 0 {
		out.Domains = make([]projects.ServiceDomain, len(in.Domains))
		for i, d := range in.Domains {
			out.Domains[i] = projects.ServiceDomain{Host: d.Host, TLS: d.TLS}
		}
	}
	if len(in.EnvVars) > 0 {
		out.EnvVars = apiv1EnvVarsToDomain(in.EnvVars)
	}
	if in.Scale != nil {
		out.Scale = &projects.ServiceScale{Min: in.Scale.Min, Max: in.Scale.Max, TargetCPU: in.Scale.TargetCPU}
	}
	if in.Sleep != nil {
		out.Sleep = &projects.ServiceSleep{Enabled: in.Sleep.Enabled, AfterMinutes: in.Sleep.AfterMinutes}
	}
	if in.Static != nil {
		out.Static = &projects.ServiceStaticSpec{
			BuilderImage: in.Static.BuilderImage,
			RuntimeImage: in.Static.RuntimeImage,
			BuildCmd:     in.Static.BuildCmd,
			OutputDir:    in.Static.OutputDir,
		}
	}
	if in.Buildpacks != nil {
		out.Buildpacks = &projects.ServiceBuildpacksSpec{
			BuilderImage:   in.Buildpacks.BuilderImage,
			LifecycleImage: in.Buildpacks.LifecycleImage,
		}
	}
	if in.Image != nil {
		out.Image = &projects.ServiceImageSpec{Repository: in.Image.Repository, Tag: in.Image.Tag}
	}
	return out
}

// apiv1EnvVarsToDomain converts the wire env-var slice to the
// domain shape. Same fields, separate type — kept distinct so
// rotating one doesn't accidentally rotate the other.
func apiv1EnvVarsToDomain(in []apiv1.EnvVar) []projects.EnvVar {
	out := make([]projects.EnvVar, len(in))
	for i, v := range in {
		out[i] = projects.EnvVar{Name: v.Name, Value: v.Value, ValueFrom: v.ValueFrom}
	}
	return out
}

// apiv1UpdateToDomain converts the wire PATCH shape to the internal
// one. Pointer semantics are preserved end-to-end: nil = leave alone,
// non-nil = apply (even when the dereferenced value is zero).
func apiv1UpdateToDomain(in apiv1.UpdateProjectRequest) projects.UpdateProjectRequest {
	out := projects.UpdateProjectRequest{
		Description: in.Description,
		BaseDomain:  in.BaseDomain,
		AlwaysOn:    in.AlwaysOn,
	}
	if in.DefaultRepo != nil {
		out.DefaultRepo = &projects.CreateProjectRepoSpec{
			URL:           in.DefaultRepo.URL,
			DefaultBranch: in.DefaultRepo.DefaultBranch,
		}
	}
	if in.GitHub != nil {
		out.GitHub = &projects.CreateProjectGithubSpec{InstallationID: in.GitHub.InstallationID}
	}
	if in.Previews != nil {
		out.Previews = &projects.UpdateProjectPreviewsSpec{
			Enabled:    in.Previews.Enabled,
			TTLDays:    in.Previews.TTLDays,
			BaseDomain: in.Previews.BaseDomain,
		}
	}
	return out
}

// ProjectsHandler wires HTTP routes onto the projects domain service.
// Svc is a ProjectsAPI (interface, not concrete) so tests can stand
// up a fake without the full projects.Service machinery. The
// Kube/Namespace/Reconciler fields back the config-as-code endpoint
// (POST /api/projects/{p}/apply); they're optional and the handler
// returns 503 when nil.
type ProjectsHandler struct {
	Svc        ProjectsAPI
	Logger     *slog.Logger
	Kube       *kube.Client
	Namespace  string
	Reconciler *spec.Reconciler
	// DB is used for the tenancy filter on /api/projects (admins
	// bypass; everyone else sees only projects they belong to).
	// Optional: when nil the filter no-ops, preserving the
	// pre-tenancy "everyone sees everything" behaviour.
	DB *db.DB
	// Audit logs sensitive mutations (env-var writes, secret writes,
	// service deletes, role grants). Optional — when nil the audit
	// calls no-op so an audit-disabled deploy still works.
	Audit *audit.Service
	// AddonReverter replays a stored addon-patch snapshot (the
	// revisions revert path for kind=addon). Optional — when nil,
	// addon revert returns 501. Satisfied by *addons.Service.
	AddonReverter AddonReverter
}

// AddonReverter is the slice of addons.Service the revisions revert
// handler needs — kept as an interface so the projects handler doesn't
// import the addons package wholesale.
type AddonReverter interface {
	RevertAddon(ctx context.Context, project, name string, patch json.RawMessage) error
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
	// Config-as-code: export the project's live state as a kuso.yaml
	// document. The result re-planned against the cluster is a no-op.
	r.Get("/api/projects/{project}/spec", h.Spec)
	r.Get("/api/projects/{project}/services/{service}/env", h.GetEnv)
	r.Post("/api/projects/{project}/services/{service}/env", h.SetEnv)
	// Env-var detection from the most recent build's source-scan
	// (env-detect init container). Returns the names + the timestamp
	// of the build that produced them — UI flags any name that's
	// referenced in source but missing from the saved env.
	r.Get("/api/projects/{project}/services/{service}/env/detected", h.GetDetectedEnv)
	// Per-service shared-secret subscription (v0.16.10). GET returns
	// the available keys grouped by source secret + the current
	// subscription. PUT replaces the subscription list outright.
	r.Get("/api/projects/{project}/services/{service}/shared-env-keys", h.GetSharedEnvKeys)
	r.Put("/api/projects/{project}/services/{service}/shared-env-keys", h.SetSharedEnvKeys)
	// Per-service addon-conn subscription (v0.16.23).
	r.Get("/api/projects/{project}/services/{service}/subscribed-addons", h.GetSubscribedAddons)
	r.Put("/api/projects/{project}/services/{service}/subscribed-addons", h.SetSubscribedAddons)
	// Per-env custom domains (v0.16.19). Edits are scoped to the
	// addressed env; no propagation to sibling envs.
	r.Put("/api/projects/{project}/services/{service}/envs/{env}/domains", h.SetEnvDomains)
	r.Post("/api/projects/{project}/services/{service}/envs/{env}/domains", h.AddEnvDomain)
	r.Delete("/api/projects/{project}/services/{service}/envs/{env}/domains/{host}", h.RemoveEnvDomain)
	// Per-env env-var overrides — write a value onto ONE env CR's envVars
	// that wins over the service-level value for that key (e.g. staging's
	// NEXT_PUBLIC_ENVIRONMENT=staging vs production's =production).
	r.Put("/api/projects/{project}/services/{service}/envs/{env}/env-vars/{name}", h.SetEnvScopedVar)
	r.Delete("/api/projects/{project}/services/{service}/envs/{env}/env-vars/{name}", h.UnsetEnvScopedVar)
	// Drift report — pending-changes surface for the service overlay.
	// Returns the list of fields that differ between the saved
	// service spec and the running env CR, plus a boolean for
	// helm-operator's reconcile lag.
	r.Get("/api/projects/{project}/services/{service}/drift", h.GetDrift)
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

	// Project-level env groups. An "env group" is the user-facing
	// "production" / "staging" / "client-demo" concept — a name that
	// spans every service + (optionally fresh) addon in the project.
	// Backed by per-service KusoEnvironment CRs labelled with
	// kuso.sislelabs.com/env=<group-name>; production is the default.
	r.Get("/api/projects/{project}/env-groups", h.ListEnvGroups)
	r.Post("/api/projects/{project}/env-groups", h.CreateEnvGroup)
	r.Get("/api/projects/{project}/env-groups/{name}", h.GetEnvGroup)
	r.Delete("/api/projects/{project}/env-groups/{name}", h.DeleteEnvGroup)
	// Per-service branch override inside a non-production env. Lets
	// the user point one service at a different branch in their
	// staging env without affecting production.
	r.Patch(
		"/api/projects/{project}/env-groups/{name}/services/{service}/branch",
		h.SetEnvGroupServiceBranch,
	)
	// Revision history. Read endpoints list/show; revert is a POST so
	// a bookmark/refresh doesn't accidentally re-apply. Kind is
	// "service" | "addon" | "environment"; name is the SHORT name.
	r.Get("/api/projects/{project}/revisions/{kind}/{name}", h.ListRevisions)
	r.Get("/api/projects/{project}/revisions/{id}", h.GetRevision)
	r.Post("/api/projects/{project}/revisions/{id}/revert", h.RevertRevision)
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
			tenancy, terr := h.DB.ListUserTenancyCached(ctx, claims.UserID)
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
	// Project creation is an instance-level action. In role-system v2
	// project:write is a per-PROJECT perm not present in any JWT, so we
	// gate creation on instance admin (settings:admin) — you must be an
	// admin to conjure a brand-new project; editors are added to
	// existing projects via grants.
	if !requireAdmin(w, r) {
		return
	}
	var wire apiv1.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Create(ctx, apiv1CreateToDomain(wire))
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
	var wire apiv1.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	out, err := h.Svc.Update(ctx, chi.URLParam(r, "project"), apiv1UpdateToDomain(wire))
	if err != nil {
		h.fail(w, "update project", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	// ?purgeData=true also wipes every PVC labeled with this
	// project — addons keep their PVCs on uninstall by default
	// (resource-policy: keep) so accidental project delete doesn't
	// turn into accidental data loss. Caller has to opt in
	// explicitly with ?purgeData=true; in the UI this maps to the
	// "delete data too" toggle in the confirm dialog.
	opts := projects.DeleteProjectOptions{
		PurgeData: r.URL.Query().Get("purgeData") == "true",
	}
	if err := h.Svc.DeleteWithOptions(ctx, project, opts); err != nil {
		h.fail(w, "delete project", err)
		return
	}
	if h.Audit != nil {
		// Project delete is the most destructive single op — it
		// fans out to every service, env, addon, build, and secret.
		// Critical-severity so an alert wired to high-severity audit
		// pages someone immediately.
		msg := fmt.Sprintf("deleted project %q", project)
		if opts.PurgeData {
			msg += " (PVCs purged)"
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "critical",
			Action:   "project.delete",
			Pipeline: project,
			Resource: "kusoproject",
			Message:  msg,
		})
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
	var wire apiv1.CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	out, err := h.Svc.AddService(ctx, chi.URLParam(r, "project"), apiv1CreateServiceToDomain(wire))
	if err != nil {
		h.fail(w, "add service", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) GetService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetService(ctx, project, chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get service", err)
		return
	}
	// The service spec carries env-var VALUES. Mask them for any caller
	// who can't read secrets (editor/viewer) — admin only sees the real
	// values. Mutates the returned CR copy in place; GetService returns a
	// fresh decode per call so this doesn't poison a cache.
	if out != nil && !callerCanReadSecrets(ctx, h.DB, project) {
		for i := range out.Spec.EnvVars {
			if out.Spec.EnvVars[i].Value != "" {
				out.Spec.EnvVars[i].Value = envMaskSentinel
			}
		}
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
	// 1 MiB hard cap. io.LimitReader honours r.Context() so a slow-
	// loris client can't camp on a goroutine for the full ReadTimeout
	// — the read unwinds the moment the context fires.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	if len(body) >= 1<<20 {
		http.Error(w, "kuso.yml too large (>1MiB)", http.StatusRequestEntityTooLarge)
		return
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	// Log the plan before executing — if the 30s context fires
	// mid-apply, the post-apply log line never runs and we'd lose all
	// trace of what was attempted.
	h.Logger.Info("apply: planned", "project", f.Project, "plan", plan.Summary())
	res, err := h.Reconciler.Apply(ctx, plan, f)
	if err != nil {
		h.Logger.Error("apply: execute", "err", err)
		http.Error(w, "apply failed", http.StatusInternalServerError)
		return
	}
	h.Logger.Info("apply", "project", f.Project, "plan", plan.Summary(), "errs", len(res.Errors))
	writeJSON(w, http.StatusOK, res)
}

// Spec returns the project's current state as a kuso.yaml document.
// GET /api/projects/{project}/spec
func (h *ProjectsHandler) Spec(w http.ResponseWriter, r *http.Request) {
	if h.Reconciler == nil {
		http.Error(w, "config-as-code disabled (kube unavailable)", http.StatusServiceUnavailable)
		return
	}
	project := chi.URLParam(r, "project")
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	f, err := spec.Export(ctx, h.Kube, h.Namespace, project)
	if err != nil {
		h.Logger.Error("spec export", "project", project, "err", err)
		http.Error(w, "export failed", http.StatusInternalServerError)
		return
	}
	out, err := yaml.Marshal(f)
	if err != nil {
		http.Error(w, "marshal failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/yaml; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	var wire apiv1.AddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	out, err := h.Svc.AddDomain(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"),
		projects.AddDomainRequest{Host: wire.Host, TLS: wire.TLS})
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
// apiv1.SetEnvVarRequest — exactly one of `value` / `secretRef`.
func (h *ProjectsHandler) SetEnvVar(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.SetEnvVarRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	name := chi.URLParam(r, "name")
	req := projects.SetEnvVarRequest{Value: wire.Value}
	if wire.SecretRef != nil {
		req.SecretRef = &projects.SetEnvVarSecretRefBody{Name: wire.SecretRef.Name, Key: wire.SecretRef.Key}
	}
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.DeleteService(ctx, project, service); err != nil {
		h.fail(w, "delete service", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "service.delete",
			Pipeline: project,
			App:      service,
			Resource: "kusoservice",
			Message:  fmt.Sprintf("deleted service %q in project %q", service, project),
		})
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	project := chi.URLParam(r, "project")
	// Editor-or-above may LIST env vars (they need the key names to set
	// values), but only admin may see the VALUES — editors get masked
	// values. requireProjectAccess admits viewer too; we still mask for
	// any non-admin, so viewer sees masked values as well.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetEnv(ctx, project, chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	masked := false
	if !callerCanReadSecrets(ctx, h.DB, project) {
		out = maskEnvValues(out)
		masked = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"envVars": out, "masked": masked})
}

// GetDrift returns the pending-changes summary for a service. Used
// by the overlay header to show "spec edited but not rolled out".
// Viewer-level access is enough — the response is just metadata
// about the spec (no secret values, no env values).
func (h *ProjectsHandler) GetDrift(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetDrift(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "drift", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetDetectedEnv returns env-var names kuso detected as referenced
// but possibly missing. Two sources, merged into one response:
//
//  1. Build-time scan: names surfaced from .env.example + source
//     grep by the env-detect init container on the most recent
//     build. High signal but stale until the next build.
//  2. Runtime crash hints: names extracted from the log shipper's
//     regex match on common "missing env" error messages
//     (KeyError, panic: missing X env, etc). Real-time.
//
// UI flags any name (from either source) that isn't in the saved
// env list, with a "Add" button to seed it. Returns:
//
//	{ names: ["DATABASE_URL", ...], detectedAt: "2026-...",
//	  hints: [{name, lastLine, lastSeen}, ...] }
func (h *ProjectsHandler) GetDetectedEnv(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
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
	var wire apiv1.SetEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	envVars := apiv1EnvVarsToDomain(wire.EnvVars)
	// Mask-sentinel guard (defense in depth). GetEnv returns masked
	// values ("••••••••") to non-admins. A read-modify-write client (web
	// editor, `kuso env set`, third-party) that didn't strip the mask
	// would echo the sentinel back here and clobber the real value. We
	// refuse any literal value equal to the sentinel rather than persist
	// it — the caller must either omit the key (leave it unchanged) or
	// supply a real value. Protects every client, not just the ones we
	// patched.
	for _, v := range envVars {
		if v.Value == envMaskSentinel {
			http.Error(w,
				fmt.Sprintf("refusing to write masked sentinel value for %q — env values are admin-only; omit the key to leave it unchanged or supply a real value", v.Name),
				http.StatusBadRequest)
			return
		}
	}
	if err := h.Svc.SetEnvWithOpts(ctx,
		project,
		service,
		envVars,
		projects.SetEnvOpts{AllowPending: wire.AllowPending},
	); err != nil {
		h.fail(w, "set env", err)
		return
	}
	if h.Audit != nil {
		// Log the names only — never the values. An env-var write is
		// a privilege event (the user can swap DATABASE_URL or wire
		// in a webhook secret), but the value itself is sensitive
		// and shouldn't sit in the audit table.
		names := make([]string, 0, len(envVars))
		for _, v := range envVars {
			names = append(names, v.Name)
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "service.setEnv",
			Pipeline: project,
			App:      service,
			Resource: "kusoservice",
			Message:  fmt.Sprintf("set %d env vars: %v (allowPending=%v)", len(names), names, wire.AllowPending),
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListPods returns the running pods for a service env. The kuso CLI
// uses this to discover the pod name before shelling in via local
// kubectl. We audit-log calls with ?reason=shell so an admin can
// reconstruct who exec'd into which pod even though the actual
// kubectl exec session never touches kuso-server.
func (h *ProjectsHandler) ListPods(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	env := r.URL.Query().Get("env")
	reason := r.URL.Query().Get("reason")
	out, err := h.Svc.ListPods(ctx, project, service, env)
	if err != nil {
		h.fail(w, "list pods", err)
		return
	}
	if h.Audit != nil && reason == "shell" {
		// Reason=shell tells us this is the CLI's `kuso shell`
		// pod-discovery call. The exec itself runs locally on the
		// caller's machine via kubectl, so this is the closest we
		// can get to a server-side audit trail without a fully
		// proxied exec endpoint.
		podNames := make([]string, 0)
		if out != nil {
			for _, p := range out.Pods {
				podNames = append(podNames, p.Name)
			}
		}
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "service.shell",
			Pipeline: project,
			App:      service,
			Resource: "kuspod",
			Message:  fmt.Sprintf("shell session opened against env=%q pods=%v", env, podNames),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Wake(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.DeleteEnvironment(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "env")); err != nil {
		h.fail(w, "delete env", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListEnvGroups returns every env-group in the project.
func (h *ProjectsHandler) ListEnvGroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.ListEnvGroups(ctx, project)
	if err != nil {
		h.fail(w, "list env-groups", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetEnvGroup returns one env-group's summary by name.
func (h *ProjectsHandler) GetEnvGroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	out, err := h.Svc.GetEnvGroup(ctx, project, chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "get env-group", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreateEnvGroup mirrors every service + (per-policy) addon into a new
// env-group. Body: {name, addonPolicy: {<addon-short>: "fresh"|"shared"}}.
func (h *ProjectsHandler) CreateEnvGroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	// Cloning every service + addon is a structural project mutation;
	// require admin rather than viewer/deployer.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	var body projects.CreateEnvGroupRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	out, err := h.Svc.CreateEnvGroup(ctx, project, body)
	if err != nil {
		h.fail(w, "create env-group", err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

// DeleteEnvGroup tears down a non-production env. Production is
// refused; preview teardown still goes through DeleteEnvironment.
// ?confirm=<name> required to acknowledge data loss (matches the addon
// delete pattern).
func (h *ProjectsHandler) DeleteEnvGroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	name := chi.URLParam(r, "name")
	if r.URL.Query().Get("confirm") != name {
		http.Error(w, "env-group delete requires ?confirm=<name> to acknowledge data loss", http.StatusBadRequest)
		return
	}
	if err := h.Svc.DeleteEnvGroup(ctx, project, name); err != nil {
		h.fail(w, "delete env-group", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetEnvGroupServiceBranch updates the branch tracked by one service
// in a non-production env. Body: {branch: "<branch-name>"}.
func (h *ProjectsHandler) SetEnvGroupServiceBranch(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	var body struct {
		Branch string `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Svc.SetServiceBranchInEnv(ctx,
		project,
		chi.URLParam(r, "name"),
		chi.URLParam(r, "service"),
		body.Branch,
	); err != nil {
		h.fail(w, "set env-group service branch", err)
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
		// Pass the wrapped message through so the UI shows
		// "env "staging" already exists" instead of bare "conflict".
		// Same pattern addons.fail uses.
		http.Error(w, err.Error(), http.StatusConflict)
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
