package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
			Path:          in.DefaultRepo.Path,
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
		Dockerfile:  in.Dockerfile,
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
		out.Repo = &projects.CreateServiceRepo{URL: in.Repo.URL, Path: in.Repo.Path, Provider: in.Repo.Provider, Token: in.Repo.Token}
	}
	if len(in.Domains) > 0 {
		out.Domains = make([]projects.ServiceDomain, len(in.Domains))
		for i, d := range in.Domains {
			out.Domains[i] = projects.ServiceDomain{Host: d.Host, TLS: d.TLS, TLSSecret: d.TLSSecret}
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
	// Release hook, build args, public env, and security context were
	// silently DROPPED by this conversion before — the wire DTO didn't
	// carry them, so callers sending them got 201 with the config
	// missing. Map them through 1:1 with the internal create shape.
	if in.Release != nil {
		out.Release = &projects.PatchReleaseRequest{
			Command:        in.Release.Command,
			TimeoutSeconds: in.Release.TimeoutSeconds,
		}
	}
	out.SnapshotBeforeDeploy = in.SnapshotBeforeDeploy
	out.BuildArgs = in.BuildArgs
	out.PublicEnv = in.PublicEnv
	if in.SecurityContext != nil {
		sc := &kube.KusoSecurityContext{
			AllowPrivilegeEscalation: in.SecurityContext.AllowPrivilegeEscalation,
		}
		if in.SecurityContext.Capabilities != nil {
			sc.Capabilities = &kube.KusoCapabilities{Add: in.SecurityContext.Capabilities.Add}
		}
		out.SecurityContext = sc
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
		Description:        in.Description,
		BaseDomain:         in.BaseDomain,
		AlwaysOn:           in.AlwaysOn,
		IncidentMonitoring: in.IncidentMonitoring,
	}
	if in.DefaultRepo != nil {
		out.DefaultRepo = &projects.CreateProjectRepoSpec{
			URL:           in.DefaultRepo.URL,
			DefaultBranch: in.DefaultRepo.DefaultBranch,
			Path:          in.DefaultRepo.Path,
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
	// FirstBuildTrigger kicks the initial build for a newly created
	// repo-backed service. Optional — when nil, service creation
	// behaves as before (the service waits for the next git push).
	// Satisfied by *builds.Service.
	//
	// Why this lives on the SERVER: AddService creates the CR and its
	// production env but never built anything, so a new service sat at
	// 0/0 until someone pushed a commit. The web wizard papered over it
	// by calling POST .../builds itself, which meant `kuso project
	// service add` and the MCP add_service tool — the two surfaces an
	// agent uses — still dead-ended silently. Triggering here fixes all
	// three at once.
	FirstBuildTrigger FirstBuildTrigger
}

// AddonReverter is the slice of addons.Service the revisions revert
// handler needs — kept as an interface so the projects handler doesn't
// import the addons package wholesale.
type AddonReverter interface {
	RevertAddon(ctx context.Context, project, name string, patch json.RawMessage) error
}

// FirstBuildTrigger is the slice of builds.Service needed to start a
// service's first build. Interface-typed so the projects handler
// doesn't take a hard dependency on the builds package.
type FirstBuildTrigger interface {
	CreateForService(ctx context.Context, project, service, branch string) error
}

// Mount registers all /api/projects/* routes onto the given router.
func (h *ProjectsHandler) Mount(r chi.Router) {
	r.Get("/api/projects", h.List)
	r.Post("/api/projects", h.Create)
	// Batched dashboard rollup: describe + metrics for every project the
	// caller can access, in ONE request. Replaces the web dashboard's
	// 2-requests-per-card fan-out (a describe + a metrics poll per
	// project). Static segment, so chi resolves it ahead of {project};
	// "summary" is a reserved project name (see projects.reservedRouteNames).
	r.Get("/api/projects/summary", h.Summary)
	r.Get("/api/projects/{project}", h.Describe)
	r.Patch("/api/projects/{project}", h.Update)
	r.Delete("/api/projects/{project}", h.Delete)
	// Project-wide hard stop / start (all services at once).
	r.Post("/api/projects/{project}/stop", h.StopProject)
	r.Post("/api/projects/{project}/start", h.StartProject)

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
	// Hard stop / start: pin to 0 replicas with no wake-on-traffic
	// (stop), then restore (start). Distinct from sleep + wake.
	r.Post("/api/projects/{project}/services/{service}/stop", h.Stop)
	r.Post("/api/projects/{project}/services/{service}/start", h.Start)
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

// maskKusoEnvVars replaces every literal env-var Value with the mask
// sentinel, in place. Names + valueFrom refs survive so the editor UI
// still shows which keys exist. Mirrors maskEnvValues (which operates on
// the projects.EnvVar wire shape) for the raw kube.KusoEnvVar carried on
// Service/Environment CRs.
func maskKusoEnvVars(vars []kube.KusoEnvVar) {
	for i := range vars {
		if vars[i].Value != "" {
			vars[i].Value = envMaskSentinel
		}
	}
}

// shortServiceName strips the "<project>-" prefix from a KusoService CR
// name to recover the user-facing short name. Service CRs are named
// <project>-<service>; the short form is what the domain enrichment
// methods (and every /services/{service} route) address.
func shortServiceName(project, crName string) string {
	prefix := project + "-"
	if strings.HasPrefix(crName, prefix) {
		return strings.TrimPrefix(crName, prefix)
	}
	return crName
}

// enrichServicesWithManagedSecretKeys surfaces managed-secret keys on
// every service in a list before the mask runs. Per-service short name is
// recovered from the CR name. Best-effort inside the domain call.
func enrichServicesWithManagedSecretKeys(ctx context.Context, svc ProjectsAPI, project string, svcs []kube.KusoService) {
	for i := range svcs {
		svc.EnrichServiceWithManagedSecretKeys(ctx, project, shortServiceName(project, svcs[i].Name), &svcs[i])
	}
}

// enrichEnvsWithManagedSecretKeys is the slice form for env lists.
func enrichEnvsWithManagedSecretKeys(ctx context.Context, svc ProjectsAPI, project string, envs []kube.KusoEnvironment) {
	for i := range envs {
		svc.EnrichEnvWithManagedSecretKeys(ctx, project, &envs[i])
	}
}

// maskServiceEnvIfNeeded masks the env-var VALUES on a service CR unless
// the caller may read secrets. Env values are admin-only (secrets:read);
// every endpoint that serializes a KusoService to the client MUST route
// it through here so the admin-only mask can't regress per-handler. The
// mutation is in place on the passed copy — callers must hand it a fresh
// decode (the domain service returns one per call), never a cached CR.
func maskServiceEnvIfNeeded(ctx context.Context, dbConn *db.DB, project string, svc *kube.KusoService) {
	if svc == nil || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	maskKusoEnvVars(svc.Spec.EnvVars)
}

// maskServicesEnvIfNeeded is the slice form of maskServiceEnvIfNeeded for
// list endpoints. The secret-read gate is resolved once (it's the same
// answer for every service in one project) and applied across the slice.
func maskServicesEnvIfNeeded(ctx context.Context, dbConn *db.DB, project string, svcs []kube.KusoService) {
	if len(svcs) == 0 || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	for i := range svcs {
		maskKusoEnvVars(svcs[i].Spec.EnvVars)
	}
}

// maskEnvIfNeeded masks the env-var VALUES on a KusoEnvironment CR unless
// the caller may read secrets. Same admin-only gate as the service form —
// an env CR carries a resolved copy of the service's env vars, so leaking
// it plaintext is the same disclosure.
func maskEnvIfNeeded(ctx context.Context, dbConn *db.DB, project string, env *kube.KusoEnvironment) {
	if env == nil || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	maskKusoEnvVars(env.Spec.EnvVars)
}

// maskEnvsIfNeeded is the slice form of maskEnvIfNeeded for list endpoints.
func maskEnvsIfNeeded(ctx context.Context, dbConn *db.DB, project string, envs []kube.KusoEnvironment) {
	if len(envs) == 0 || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	for i := range envs {
		maskKusoEnvVars(envs[i].Spec.EnvVars)
	}
}

// redactRepoRefCreds returns a clone of ref with embedded URL
// credentials removed, or ref itself when there's nothing to strip.
// Clone (not in-place mutate) because the ref pointer may be shared
// with a fresh decode the caller still owns elsewhere.
func redactRepoRefCreds(ref *kube.KusoRepoRef) *kube.KusoRepoRef {
	if ref == nil || !kube.RepoURLHasCredentials(ref.URL) {
		return ref
	}
	cp := *ref
	cp.URL = kube.StripRepoURLCredentials(ref.URL)
	return &cp
}

// redactProjectRepoIfNeeded strips credentials from a project's
// defaultRepo URL unless the caller may read secrets on that project.
// Deploy-token URLs (https://user:gldt-xxx@gitlab.com/…) are working
// clone credentials; kuso treats secret VALUES as admin-only
// (secrets:read), and a token in a repo URL is exactly that. Same
// contract as maskServiceEnvIfNeeded: every endpoint serializing a
// KusoProject must route through here.
func redactProjectRepoIfNeeded(ctx context.Context, dbConn *db.DB, p *kube.KusoProject) {
	if p == nil || callerCanReadSecrets(ctx, dbConn, p.Name) {
		return
	}
	p.Spec.DefaultRepo = redactRepoRefCreds(p.Spec.DefaultRepo)
}

// redactServicesRepoIfNeeded is the service-slice form (one gate check
// per project, like maskServicesEnvIfNeeded).
func redactServicesRepoIfNeeded(ctx context.Context, dbConn *db.DB, project string, svcs []kube.KusoService) {
	if len(svcs) == 0 || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	for i := range svcs {
		svcs[i].Spec.Repo = redactRepoRefCreds(svcs[i].Spec.Repo)
	}
}

// redactServiceRepoIfNeeded is the single-service form.
func redactServiceRepoIfNeeded(ctx context.Context, dbConn *db.DB, project string, svc *kube.KusoService) {
	if svc == nil || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	svc.Spec.Repo = redactRepoRefCreds(svc.Spec.Repo)
}

// filterProjectsForCaller applies the /api/projects tenancy filter to a
// full CR list: non-admins only see projects they have a
// ProjectMembership on; admins (settings:admin) bypass with the full
// list. Pending users get an empty slice — they're auth'd but invisible
// to the rest of the system. Shared by List and Summary so the batched
// dashboard endpoint can never disclose more than the list it batches.
//
// On a tenancy-resolution error the response is already written (500)
// and ok=false is returned — fail CLOSED: skipping the filter would
// hand a non-admin the full project list (names + specs). A 500 is
// honest and retryable; the silent full-list disclosure is neither.
func (h *ProjectsHandler) filterProjectsForCaller(ctx context.Context, w http.ResponseWriter, all []kube.KusoProject) ([]kube.KusoProject, bool) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok || auth.Has(claims.Permissions, auth.PermSettingsAdmin) || h.DB == nil {
		return all, true
	}
	tenancy, terr := h.DB.ListUserTenancyCached(ctx, claims.UserID)
	if terr != nil {
		h.Logger.Error("list projects: tenancy filter unavailable", "user", claims.UserID, "err", terr)
		writeErr(w, http.StatusInternalServerError, "internal")
		return nil, false
	}
	allowed := map[string]struct{}{}
	for _, m := range tenancy.ProjectMemberships {
		allowed[m.Project] = struct{}{}
	}
	filtered := all[:0]
	for _, p := range all {
		if _, ok := allowed[p.Name]; ok {
			filtered = append(filtered, p)
		}
	}
	return filtered, true
}

func (h *ProjectsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "list projects", err)
		return
	}
	out, ok := h.filterProjectsForCaller(ctx, w, out)
	if !ok {
		return
	}
	// Strip repo-URL credentials per project (the gate differs per
	// project: a caller can be admin on one and viewer on another).
	// Tenancy is cached, so this is N map lookups, not N queries.
	for i := range out {
		redactProjectRepoIfNeeded(ctx, h.DB, &out[i])
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Project creation is an instance-level action: project:write is a
	// per-PROJECT perm not present in any JWT, and there is no project
	// to be a member of yet. Gate on projects:create — carried by
	// instance ADMINS and instance EDITORS (self-serve; they become
	// project-admin of what they create via the grant below). The
	// settings:admin alternative keeps pre-resolver admin tokens
	// working on wiring that has no PermissionResolver installed.
	claims, ok := auth.ClaimsFromContext(r.Context())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !auth.HasAny(claims.Permissions, auth.PermProjectsCreate, auth.PermSettingsAdmin) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	var wire apiv1.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	out, err := h.Svc.Create(ctx, apiv1CreateToDomain(wire))
	if err != nil {
		h.fail(w, "create project", err)
		return
	}
	// Self-grant: a non-admin creator gets project-admin on the project
	// they just created — otherwise it would be invisible to them (no
	// grant → ProjectRoleFor returns ""). Instance admins skip this;
	// they're implicitly admin-on-every-project. Best-effort: a failed
	// grant leaves a project only admins can see, which the error nudges
	// the operator to fix; it must not roll back the created project.
	if h.DB != nil && !auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		if _, gerr := h.DB.AddProjectGrant(ctx, out.Name, claims.UserID, "", db.ProjectRoleAdmin); gerr != nil {
			h.Logger.Error("create project: self-grant failed — project visible to admins only",
				"project", out.Name, "user", claims.UserID, "err", gerr)
		} else {
			h.DB.EvictUserTenancy(claims.UserID)
		}
	}
	// The creator is normally project-admin (self-grant above) so this
	// is a no-op for them; it matters when the self-grant failed.
	redactProjectRepoIfNeeded(ctx, h.DB, out)
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) Describe(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	project := chi.URLParam(r, "project")
	out, err := h.Svc.Describe(ctx, project)
	if err != nil {
		h.fail(w, "describe project", err)
		return
	}
	// The rollup embeds full Service + Environment CRs, both carrying
	// env-var VALUES. Mask them for callers who can't read secrets — the
	// same admin-only gate GetService/GetEnv enforce.
	if out != nil {
		// Surface managed-secret keys as name-only entries BEFORE masking
		// so they list alongside literal env vars (masked uniformly).
		enrichServicesWithManagedSecretKeys(ctx, h.Svc, project, out.Services)
		enrichEnvsWithManagedSecretKeys(ctx, h.Svc, project, out.Environments)
		maskServicesEnvIfNeeded(ctx, h.DB, project, out.Services)
		maskEnvsIfNeeded(ctx, h.DB, project, out.Environments)
		redactProjectRepoIfNeeded(ctx, h.DB, out.Project)
		redactServicesRepoIfNeeded(ctx, h.DB, project, out.Services)
	}
	writeJSON(w, http.StatusOK, out)
}

// projectSummaryItem is one dashboard card's worth of data in the
// batched GET /api/projects/summary response. Project/Services/
// Environments carry the exact same shape (and the same enrich + mask +
// redact treatment) as GET /api/projects/{project}; Metrics is the same
// rollup GET /api/projects/{project}/metrics returns, so clients can
// swap N describe + N metrics requests for one array without new types.
type projectSummaryItem struct {
	Project      *kube.KusoProject      `json:"project"`
	Services     []kube.KusoService     `json:"services"`
	Environments []kube.KusoEnvironment `json:"environments"`
	Metrics      projectMetricsResponse `json:"metrics"`
}

// Summary is GET /api/projects/summary — the batched projects-dashboard
// endpoint. One item per project the caller can access (same tenancy
// filter as List, same fail-closed contract). Per-project describes ride
// the informer-cached CR lists and the metrics rollup rides the shared
// singleflight+TTL pod-metrics cache (metrics_cache.go), so the whole
// response costs roughly ONE describe + one metrics fetch per involved
// namespace — not 2N HTTP round-trips from the browser.
func (h *ProjectsHandler) Summary(w http.ResponseWriter, r *http.Request) {
	// Bigger budget than the single-project 5s projectCtx: the loop
	// below touches every accessible project. Describes are cache-served
	// so this is generous, not load-bearing.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	all, err := h.Svc.List(ctx)
	if err != nil {
		h.fail(w, "projects summary", err)
		return
	}
	visible, ok := h.filterProjectsForCaller(ctx, w, all)
	if !ok {
		return
	}
	items := make([]projectSummaryItem, 0, len(visible))
	for i := range visible {
		name := visible[i].Name
		d, derr := h.Svc.Describe(ctx, name)
		if derr != nil || d == nil {
			// Best-effort per card: one broken project must not blank the
			// whole dashboard. The card renders name-only (same as a
			// failed per-card describe today); the miss is logged so it
			// doesn't hide.
			h.Logger.Warn("projects summary: describe failed; card degraded",
				"project", name, "err", derr)
			items = append(items, projectSummaryItem{
				Project: &visible[i],
				Metrics: projectMetricsResponse{Project: name},
			})
			continue
		}
		// Same serialization treatment as the Describe handler: surface
		// managed-secret keys BEFORE masking, then mask env values and
		// strip repo credentials for callers without secrets:read.
		enrichServicesWithManagedSecretKeys(ctx, h.Svc, name, d.Services)
		enrichEnvsWithManagedSecretKeys(ctx, h.Svc, name, d.Environments)
		maskServicesEnvIfNeeded(ctx, h.DB, name, d.Services)
		maskEnvsIfNeeded(ctx, h.DB, name, d.Environments)
		redactProjectRepoIfNeeded(ctx, h.DB, d.Project)
		redactServicesRepoIfNeeded(ctx, h.DB, name, d.Services)
		ns := visible[i].Spec.Namespace
		if ns == "" {
			ns = h.Namespace
		}
		items = append(items, projectSummaryItem{
			Project:      d.Project,
			Services:     d.Services,
			Environments: d.Environments,
			Metrics:      h.summaryMetrics(ctx, name, ns, d.Environments),
		})
	}
	writeJSON(w, http.StatusOK, items)
}

// summaryMetrics computes the per-project CPU/mem rollup for the summary
// endpoint from the envs the describe already fetched — same semantics
// as KubernetesHandler.ProjectMetrics (production env-group only, so
// previews coming and going don't jitter the card numbers) and the same
// cached namespace-wide pod-metrics fetch, so a dashboard poll costs one
// upstream metrics-server query per namespace, not one per project.
func (h *ProjectsHandler) summaryMetrics(ctx context.Context, project, ns string, envs []kube.KusoEnvironment) projectMetricsResponse {
	out := projectMetricsResponse{Project: project}
	// Scope to the PRODUCTION env-GROUP via the env label. spec.kind=
	// "production" is also set on staging clones, so keying on it would
	// inflate the prod card with clone metrics (mirrors ProjectMetrics).
	prod := make(map[string]struct{})
	for i := range envs {
		if envs[i].Labels[kube.LabelEnv] == "production" {
			prod[envs[i].Name] = struct{}{}
		}
	}
	out.Envs = len(prod)
	if len(prod) == 0 || h.Kube == nil {
		return out
	}
	items, ok := listPodMetricsCached(ctx, h.Kube, ns)
	if !ok {
		// metrics-server missing or transient outage — zeros, the card
		// renders its "—" state. Same graceful fall-through as the
		// per-project metrics endpoint.
		return out
	}
	for i := range items {
		if _, mine := prod[podMetricsInstance(items[i])]; !mine {
			continue
		}
		if sumPodMetricsUsage(items[i], &out.CPUm, &out.MemBytes) {
			out.Pods++
		}
	}
	return out
}

// Update is PATCH /api/projects/{project}. Body is a partial spec —
// see projects.UpdateProjectRequest. Pointer fields distinguish unset
// from set-to-zero so callers can explicitly toggle previews.enabled.
func (h *ProjectsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.UpdateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
	redactProjectRepoIfNeeded(ctx, h.DB, out)
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	// Admin, not editor: this is the single most destructive op in the
	// API (fans out to every service/env/addon/build/secret, and
	// ?purgeData=true takes the PVCs). Editors deploy and configure;
	// removing the whole project — data included — is an owner-level
	// decision. Project creators hold admin via the create self-grant,
	// so self-serve delete of your own project still works.
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleAdmin) {
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
	// DB state scoped to the project dies with it. Grants especially:
	// a stale ProjectGrant row resurrects its holder as project-admin
	// the moment anyone recreates a project under this name (the
	// create-time self-grant makes stale rows the default, not the
	// exception). Best-effort — the kube delete already happened, so
	// log-and-continue beats failing the request. Fresh context: a
	// slow fan-out delete can eat most of the request's 5s budget,
	// and running the cleanup on the dregs would silently re-open the
	// stale-grant window on exactly the big projects where it hurts.
	if h.DB != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if n, gerr := h.DB.RemoveProjectGrantsForProject(ctx, project); gerr != nil {
			h.Logger.Error("delete project: removing grants failed — stale rows will re-attach to a reborn project name",
				"project", project, "err", gerr)
		} else if n > 0 {
			h.Logger.Info("delete project: removed grants", "project", project, "count", n)
		}
		if merr := h.DB.ClearProjectNotificationMute(ctx, project); merr != nil {
			h.Logger.Error("delete project: clearing notification mute failed — a reborn project name starts muted",
				"project", project, "err", merr)
		}
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
	project := chi.URLParam(r, "project")
	out, err := h.Svc.ListServices(ctx, project)
	if err != nil {
		h.fail(w, "list services", err)
		return
	}
	enrichServicesWithManagedSecretKeys(ctx, h.Svc, project, out)
	maskServicesEnvIfNeeded(ctx, h.DB, project, out)
	redactServicesRepoIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusOK, out)
}

func (h *ProjectsHandler) AddService(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.CreateServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	out, err := h.Svc.AddService(ctx, project, apiv1CreateServiceToDomain(wire))
	if err != nil {
		h.fail(w, "add service", err)
		return
	}
	if out != nil {
		h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, shortServiceName(project, out.Name), out)
	}
	firstBuild := h.triggerFirstBuild(ctx, project, out)
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
	// Additive envelope: same KusoService fields as before, plus an
	// optional firstBuild block so the caller learns whether the
	// auto-triggered first build actually started (previously the
	// failure was server-log-only and the 201 carried no signal).
	if out == nil {
		// Preserve the pre-envelope "null" body for a nil service.
		writeJSON(w, http.StatusCreated, out)
		return
	}
	writeJSON(w, http.StatusCreated, createServiceResponse{
		KusoService: out,
		FirstBuild:  firstBuild,
	})
}

// createServiceResponse embeds the created service (inlined in JSON —
// wire-compatible with the pre-envelope response) and adds the
// optional first-build outcome. firstBuild is absent entirely when
// there was nothing to build (image/worker runtime, no repo).
type createServiceResponse struct {
	*kube.KusoService
	FirstBuild *firstBuildResult `json:"firstBuild,omitempty"`
}

type firstBuildResult struct {
	Triggered bool   `json:"triggered"`
	Error     string `json:"error,omitempty"`
}

// triggerFirstBuild kicks the initial build for a newly created
// repo-backed service so it actually deploys instead of sitting at 0/0
// until the next git push. See ProjectsHandler.FirstBuildTrigger.
//
// Skipped for runtimes with nothing to build:
//   - "image" deploys a pre-built tag straight from a registry.
//   - "worker" reuses its fromService sibling's image and has no repo
//     of its own (builds.Create rejects it outright).
//
// Also skipped when the service has no repo at all, which is the
// same "nothing to build from" condition expressed on the CR.
//
// Best-effort by design: the service is already created and the 201 has
// to reflect that. A build that fails to start is logged AND surfaced
// on the create response's firstBuild field — the user can retry with
// `kuso build trigger`. Returns nil when there was nothing to build.
func (h *ProjectsHandler) triggerFirstBuild(ctx context.Context, project string, svc *kube.KusoService) *firstBuildResult {
	if h.FirstBuildTrigger == nil || svc == nil {
		return nil
	}
	switch svc.Spec.Runtime {
	case "image", "worker":
		return nil
	}
	if svc.Spec.Repo == nil || svc.Spec.Repo.URL == "" {
		return nil
	}
	branch := svc.Spec.Repo.DefaultBranch
	short := shortServiceName(project, svc.Name)
	if err := h.FirstBuildTrigger.CreateForService(ctx, project, short, branch); err != nil {
		if h.Logger != nil {
			h.Logger.Warn("first build for new service did not start (service was created; retry with `kuso build trigger`)",
				"project", project, "service", short, "branch", branch, "err", err)
		}
		return &firstBuildResult{Triggered: false, Error: err.Error()}
	}
	return &firstBuildResult{Triggered: true}
}

func (h *ProjectsHandler) GetService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	service := chi.URLParam(r, "service")
	out, err := h.Svc.GetService(ctx, project, service)
	if err != nil {
		h.fail(w, "get service", err)
		return
	}
	// Surface managed-secret keys (name-only) BEFORE masking so orphaned
	// secret keys list as first-class env vars in the editor.
	h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, service, out)
	// The service spec carries env-var VALUES. Mask them for any caller
	// who can't read secrets (editor/viewer) — admin only sees the real
	// values. Mutates the returned CR copy in place; GetService returns a
	// fresh decode per call so this doesn't poison a cache.
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
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
		writeErr(w, http.StatusServiceUnavailable, "config-as-code disabled (kube unavailable)")
		return
	}
	// 1 MiB hard cap. io.LimitReader honours r.Context() so a slow-
	// loris client can't camp on a goroutine for the full ReadTimeout
	// — the read unwinds the moment the context fires.
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body")
		return
	}
	if len(body) >= 1<<20 {
		writeErr(w, http.StatusRequestEntityTooLarge, "kuso.yml too large (>1MiB)")
		return
	}
	f, err := spec.Parse(body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if f.Project != chi.URLParam(r, "project") {
		writeErr(w, http.StatusBadRequest, "project name in YAML doesn't match URL")
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
		writeErr(w, http.StatusInternalServerError, "plan failed")
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
	// ?rotateSecrets=1 forces generated secrets to be re-minted (the
	// deliberate escape hatch); the default is generate-once.
	opts := spec.ApplyOpts{RotateSecrets: r.URL.Query().Get("rotateSecrets") == "1"}
	res, err := h.Reconciler.Apply(ctx, plan, f, opts)
	if err != nil {
		h.Logger.Error("apply: execute", "err", err)
		writeErr(w, http.StatusInternalServerError, "apply failed")
		return
	}
	h.Logger.Info("apply", "project", f.Project, "plan", plan.Summary(), "errs", len(res.Errors))
	writeJSON(w, http.StatusOK, res)
}

// Spec returns the project's current state as a kuso.yaml document.
// GET /api/projects/{project}/spec
func (h *ProjectsHandler) Spec(w http.ResponseWriter, r *http.Request) {
	if h.Reconciler == nil {
		writeErr(w, http.StatusServiceUnavailable, "config-as-code disabled (kube unavailable)")
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
		writeErr(w, http.StatusInternalServerError, "export failed")
		return
	}
	// Mask literal env VALUES for callers who can't read secrets — the
	// same gate GetService/GetEnv enforce. Without this, the config-export
	// endpoint is a plaintext-secret read for any viewer, bypassing the
	// mask everywhere else. ${{ }} varrefs and {generate:} directives are
	// references, not values, so they stay verbatim (they leak nothing).
	if f != nil && !callerCanReadSecrets(ctx, h.DB, project) {
		for si := range f.Services {
			// Repo URLs can embed deploy-token credentials — same
			// admin-only disclosure as env values.
			f.Services[si].Repo = kube.StripRepoURLCredentials(f.Services[si].Repo)
			for k, ev := range f.Services[si].Env {
				if ev.Generate != "" {
					continue // a generator directive, not a value
				}
				// Skip ONLY a valid, fully-closed ${{ name.KEY }} reference —
				// that's a pointer, not a secret value. A literal that merely
				// STARTS with "${{" but isn't a closed ref (e.g. "${{ oops")
				// is a real value stored verbatim and MUST be masked; a bare
				// HasPrefix("${{") check would leak it. ParseVarRef returns
				// ok=true only for the closed pure-ref form.
				if _, isRef, _ := projects.ParseVarRef(strings.TrimSpace(ev.Value)); isRef {
					continue
				}
				if ev.Value != "" {
					ev.Value = envMaskSentinel
					f.Services[si].Env[k] = ev
				}
			}
		}
	}
	out, err := yaml.Marshal(f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "marshal failed")
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
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	out, err := h.Svc.PatchService(ctx, project, service, req)
	if err != nil {
		h.fail(w, "patch service", err)
		return
	}
	h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, service, out)
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusOK, out)
}

// AddDomain appends a single domain to a service's spec.domains. Body
// is projects.AddDomainRequest. The mutation is per-service serialised
// so two concurrent adds don't race.
func (h *ProjectsHandler) AddDomain(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.AddDomainRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	out, err := h.Svc.AddDomain(ctx, project, service,
		projects.AddDomainRequest{Host: wire.Host, TLS: wire.TLS, TLSSecret: wire.TLSSecret})
	if err != nil {
		h.fail(w, "add domain", err)
		return
	}
	h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, service, out)
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
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
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	out, err := h.Svc.RemoveDomain(ctx,
		project,
		service,
		chi.URLParam(r, "host"))
	if err != nil {
		h.fail(w, "remove domain", err)
		return
	}
	h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, service, out)
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusOK, out)
}

// SetEnvVar adds or overwrites a single env var by name. Body is
// apiv1.SetEnvVarRequest — exactly one of `value` / `secretRef`.
func (h *ProjectsHandler) SetEnvVar(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.SetEnvVarRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
	// Mask-sentinel guard (defense in depth) — same invariant the bulk
	// SetEnv path enforces. GetEnv returns masked values ("••••••••") to
	// non-admins; a read-modify-write client that didn't strip the mask
	// would echo the sentinel back here and clobber the real value. Refuse
	// the literal sentinel rather than persist it.
	if wire.Value == envMaskSentinel || (wire.SecretValue != nil && *wire.SecretValue == envMaskSentinel) {
		writeErr(w,

			http.StatusBadRequest, fmt.Sprintf("refusing to write masked sentinel value for %q — env values are admin-only; supply a real value", name))

		return
	}
	// Unified "one secret primitive" write: the server decides storage.
	var out *kube.KusoService
	var err error
	if wire.Auto {
		out, err = h.Svc.SetEnvValue(ctx, project, service, name, wire.Value)
	} else {
		req := projects.SetEnvVarRequest{Value: wire.Value}
		if wire.SecretRef != nil {
			req.SecretRef = &projects.SetEnvVarSecretRefBody{Name: wire.SecretRef.Name, Key: wire.SecretRef.Key}
		}
		if wire.SecretValue != nil {
			req.SecretValue = wire.SecretValue
		}
		out, err = h.Svc.SetEnvVar(ctx, project, service, name, req)
	}
	if err != nil {
		h.fail(w, "set env var", err)
		return
	}
	// Clear any pending crash hint for this var: the user just set
	// it, so the "your last crash mentioned X" pip should disappear
	// without waiting for the next crash to confirm. Best-effort.
	if h.DB != nil {
		_ = h.DB.DeleteEnvHint(ctx, project, service, name)
	}
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
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
	project := chi.URLParam(r, "project")
	out, err := h.Svc.UnsetEnvVar(ctx,
		project,
		chi.URLParam(r, "service"),
		chi.URLParam(r, "name"))
	if err != nil {
		h.fail(w, "unset env var", err)
		return
	}
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
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
		writeErr(w, http.StatusBadRequest, "newName required")
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
	project := chi.URLParam(r, "project")
	out, err := h.Svc.RenameService(ctx,
		project,
		chi.URLParam(r, "service"),
		req.NewName,
	)
	if err != nil {
		h.fail(w, "rename service", err)
		return
	}
	if out != nil {
		h.Svc.EnrichServiceWithManagedSecretKeys(ctx, project, shortServiceName(project, out.Name), out)
	}
	maskServiceEnvIfNeeded(ctx, h.DB, project, out)
	redactServiceRepoIfNeeded(ctx, h.DB, project, out)
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
	canReadSecrets := callerCanReadSecrets(ctx, h.DB, project)
	// ?reveal=true resolves EVERY value to plaintext (managed secrets +
	// addon/shared secretKeyRefs), the admin-only "view" path. It is gated
	// on secrets:read — a non-admin asking to reveal still gets masked
	// values (the request is honored only for those allowed to see them).
	reveal := r.URL.Query().Get("reveal") == "true" && canReadSecrets
	var out []projects.EnvVar
	var err error
	// ?env=<name> returns that ONE environment's overrides (what
	// `kuso env set --env` writes) instead of the service-level list. These
	// were previously unreadable anywhere, so an override could pin a value
	// on production that no view showed. Overrides are returned as stored:
	// literals as-is, refs as their target; reveal isn't applied here.
	if envName := r.URL.Query().Get("env"); envName != "" {
		out, err = h.Svc.GetEnvScoped(ctx, project, chi.URLParam(r, "service"), envName)
		reveal = false
	} else if reveal {
		out, err = h.Svc.GetEnvRevealed(ctx, project, chi.URLParam(r, "service"))
	} else {
		out, err = h.Svc.GetEnv(ctx, project, chi.URLParam(r, "service"))
	}
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	masked := false
	if !canReadSecrets {
		out = maskEnvValues(out)
		masked = true
	}
	writeJSON(w, http.StatusOK, map[string]any{"envVars": out, "masked": masked, "revealed": reveal})
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
	var hints any
	if h.DB != nil {
		hints, _ = h.DB.ListEnvHints(ctx, project, service)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"names":      names,
		"detectedAt": ts,
		"hints":      hints,
	})
}

func (h *ProjectsHandler) SetEnv(w http.ResponseWriter, r *http.Request) {
	var wire apiv1.SetEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
			writeErr(w,

				http.StatusBadRequest, fmt.Sprintf("refusing to write masked sentinel value for %q — env values are admin-only; omit the key to leave it unchanged or supply a real value", v.Name))

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

// Stop hard-stops a service: pins every env to 0 replicas AND stops the
// activator from waking it on traffic (unlike sleep). Editor-gated,
// audited. Idempotent.
func (h *ProjectsHandler) Stop(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.StopService(ctx, project, service); err != nil {
		h.fail(w, "stop service", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "service.stop",
			Pipeline: project,
			App:      service,
			Resource: "kusoservice",
			Message:  fmt.Sprintf("service %s/%s hard-stopped (0 replicas, no wake-on-traffic)", project, service),
		})
	}
	w.WriteHeader(http.StatusAccepted)
}

// Start clears a hard-stop: the operator scales the deployment back to
// its configured replica count and normal wake-on-traffic resumes.
// Editor-gated, audited. Idempotent.
func (h *ProjectsHandler) Start(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project, service := chi.URLParam(r, "project"), chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.StartService(ctx, project, service); err != nil {
		h.fail(w, "start service", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "service.start",
			Pipeline: project,
			App:      service,
			Resource: "kusoservice",
			Message:  fmt.Sprintf("service %s/%s started (hard-stop cleared)", project, service),
		})
	}
	w.WriteHeader(http.StatusAccepted)
}

// StopProject hard-stops every service in a project. Editor-gated,
// audited. Best-effort — a per-service failure surfaces as a 5xx naming
// the stragglers; already-applied stops stick.
func (h *ProjectsHandler) StopProject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.StopProject(ctx, project); err != nil {
		h.fail(w, "stop project", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "warn",
			Action:   "project.stop",
			Pipeline: project,
			Resource: "kusoproject",
			Message:  fmt.Sprintf("project %s hard-stopped (all services → 0 replicas)", project),
		})
	}
	w.WriteHeader(http.StatusAccepted)
}

// StartProject clears the hard-stop on every service in a project.
// Editor-gated, audited.
func (h *ProjectsHandler) StartProject(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.Svc.StartProject(ctx, project); err != nil {
		h.fail(w, "start project", err)
		return
	}
	if h.Audit != nil {
		h.Audit.Log(ctx, audit.Entry{
			User:     auditUser(ctx),
			Severity: "info",
			Action:   "project.start",
			Pipeline: project,
			Resource: "kusoproject",
			Message:  fmt.Sprintf("project %s started (all services)", project),
		})
	}
	w.WriteHeader(http.StatusAccepted)
}

func (h *ProjectsHandler) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	project := chi.URLParam(r, "project")
	out, err := h.Svc.ListEnvironments(ctx, project)
	if err != nil {
		h.fail(w, "list envs", err)
		return
	}
	enrichEnvsWithManagedSecretKeys(ctx, h.Svc, project, out)
	maskEnvsIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusOK, out)
}

// AddEnvironment creates a custom env (e.g. staging on a branch).
// Production envs auto-create with the service; preview envs come
// from the GH PR webhook; this is the "third kind" — long-lived,
// branch-bound, with its own URL.
func (h *ProjectsHandler) AddEnvironment(w http.ResponseWriter, r *http.Request) {
	var req projects.CreateEnvRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	project := chi.URLParam(r, "project")
	out, err := h.Svc.AddEnvironment(ctx,
		project,
		chi.URLParam(r, "service"),
		req,
	)
	if err != nil {
		h.fail(w, "add environment", err)
		return
	}
	h.Svc.EnrichEnvWithManagedSecretKeys(ctx, project, out)
	maskEnvIfNeeded(ctx, h.DB, project, out)
	writeJSON(w, http.StatusCreated, out)
}

func (h *ProjectsHandler) GetEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := projectCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	project := chi.URLParam(r, "project")
	out, err := h.Svc.GetEnvironment(ctx, project, chi.URLParam(r, "env"))
	if err != nil {
		h.fail(w, "get env", err)
		return
	}
	h.Svc.EnrichEnvWithManagedSecretKeys(ctx, project, out)
	maskEnvIfNeeded(ctx, h.DB, project, out)
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
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
		writeErr(w, http.StatusBadRequest, "env-group delete requires ?confirm=<name> to acknowledge data loss")
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
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
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
		writeErr(w, http.StatusNotFound, notFoundMsg(err, projects.ErrNotFound, kindFromOp(op)))
	case errors.Is(err, projects.ErrConflict):
		// Pass the wrapped message through so the UI shows
		// "env "staging" already exists" instead of bare "conflict".
		// Same pattern addons.fail uses.
		writeErr(w, http.StatusConflict, err.Error())
	case errors.Is(err, projects.ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, projects.ErrCompositeVarRef):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, projects.ErrUnknownVarRef):
		// A ref naming something we can't resolve is the caller's typo, not a
		// server fault. As a 500 the CLI printed "internal" while the message
		// naming the failed ref stayed in the server log.
		writeErr(w, http.StatusBadRequest, err.Error())
	default:
		h.Logger.Error("projects handler", "op", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}

// writeJSON encodes v as JSON with the given status. Encoding errors are
// logged but not bubbled, since the response headers are already sent.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
