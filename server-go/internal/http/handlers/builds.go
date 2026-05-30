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
	"kuso/server/internal/builds"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// BuildsHandler exposes the build list + trigger routes for a service.
type BuildsHandler struct {
	Svc    *builds.Service
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers builds routes onto the given chi router.
func (h *BuildsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/builds", h.List)
	r.Post("/api/projects/{project}/services/{service}/builds", h.Create)
	// One-click rollback: re-point the production env at a previous
	// successful build's image. The user picks the build by name (CR
	// name); we patch spec.image to that build's image tag.
	r.Post("/api/projects/{project}/services/{service}/builds/{build}/rollback", h.Rollback)
	// Cancel an in-flight build: marks it cancelled + tears down the
	// kaniko Job. Coolify-equivalent: lets the user unjam a wedged build
	// without ssh. Returns 400 if the build is already in a terminal
	// phase (succeeded/failed/cancelled) — there's nothing to stop.
	r.Post("/api/projects/{project}/services/{service}/builds/{build}/cancel", h.Cancel)
	// Project-scoped "latest build per service" — used by the canvas
	// to color service nodes by their pending/failed/succeeded build
	// status without N round-trips. Returns a map keyed by short
	// service name.
	r.Get("/api/projects/{project}/builds/latest", h.LatestPerService)
}

// LatestPerService returns {<serviceShortName>: buildSummary} for the
// project. Newest build wins per service; services with no builds are
// omitted from the map.
//
// Why "short" name (e.g. "hello") vs the FQ "<proj>-<svc>": the canvas
// already has the short name on each node and looking it up by the
// fully-qualified form means stripping the project prefix client-side.
// Doing the strip server-side keeps the consumer dumb.
func (h *BuildsHandler) LatestPerService(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	// Env-aware filter (B1.2 from v0.17.0 audit). Pre-fix the handler
	// returned the newest build globally per service, so a staging
	// build that landed after a production build masked production's
	// status on the canvas. Now an `?env=<name>` query param filters
	// to the env-group's branch (production = the project's default
	// branch; staging = `staging` by convention; preview-pr-N = the
	// matching pr-N build). Empty env = legacy "newest globally"
	// behaviour preserved for any external caller that hasn't been
	// updated.
	envFilter := r.URL.Query().Get("env")

	raw, err := h.Svc.List(ctx, project, "")
	if err != nil {
		h.fail(w, "list project builds", err)
		return
	}
	// Build the branch-allow predicate, per service. Production uses
	// the project's default branch; preview-pr-N looks up each
	// service's env CR and uses its spec.branch (the PR's head ref);
	// custom envs (staging, qa, ...) use the env CR's branch when set
	// or fall back to matching the env name as the branch.
	//
	// Per-service because different services within the same env can
	// (in theory) track different branches — buildAllowedByService
	// caches the answer per service FQN so we make at most one env
	// fetch per service in the loop, not N×M.
	type predFn func(branch string) bool
	allowAny := func(_ string) bool { return true }
	branchAllowed := allowAny
	var defaultBranch string
	if envFilter == "production" {
		proj, perr := h.Svc.Kube.GetKusoProject(ctx, h.Svc.Namespace, project)
		defaultBranch = "main"
		if perr == nil && proj != nil && proj.Spec.DefaultRepo != nil && proj.Spec.DefaultRepo.DefaultBranch != "" {
			defaultBranch = proj.Spec.DefaultRepo.DefaultBranch
		}
		expect := defaultBranch
		branchAllowed = func(b string) bool { return b == expect || b == "" }
	}
	// For non-production filters we resolve per service inside the
	// loop. Cache: serviceFQN → predicate.
	serviceBranchAllowed := map[string]predFn{}
	resolveForService := func(serviceFQN string) predFn {
		if envFilter == "" || envFilter == "production" {
			return branchAllowed
		}
		if cached, ok := serviceBranchAllowed[serviceFQN]; ok {
			return cached
		}
		envName := serviceFQN + "-" + envFilter
		env, _ := h.Svc.Kube.GetKusoEnvironment(ctx, h.Svc.Namespace, envName)
		var pred predFn
		if env != nil && env.Spec.Branch != "" {
			expect := env.Spec.Branch
			pred = func(b string) bool { return b == expect || b == "" }
		} else {
			// Env CR missing or branch unset — fall back to matching
			// the env name as the branch (staging env tracking the
			// "staging" branch is the common case).
			expect := envFilter
			pred = func(b string) bool { return b == expect || b == "" }
		}
		serviceBranchAllowed[serviceFQN] = pred
		return pred
	}
	// Newest-first ordering already enforced by Service.List, so the
	// first matching build we see for a given service IS the latest.
	seen := map[string]bool{}
	out := map[string]buildSummary{}
	for _, b := range raw {
		fq := b.Spec.Service
		short := fq
		if prefix := project + "-"; len(fq) > len(prefix) && fq[:len(prefix)] == prefix {
			short = fq[len(prefix):]
		}
		if seen[short] {
			continue
		}
		pred := resolveForService(fq)
		if !pred(b.Spec.Branch) {
			continue
		}
		seen[short] = true
		out[short] = toBuildSummary(b)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *BuildsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	err := h.Svc.Cancel(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "build"))
	if err != nil {
		h.fail(w, "cancel build", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *BuildsHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	// Env scope from ?env=<name>. Empty defaults to "production" in
	// the service layer, matching pre-v0.17.1 callers that always
	// rolled back the production env.
	envName := r.URL.Query().Get("env")
	out, err := h.Svc.Rollback(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), envName, chi.URLParam(r, "build"))
	if err != nil {
		// Reuse the existing fail() — handles phase + missing-image
		// errors as 400, missing build as 404.
		h.fail(w, "rollback build", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func buildsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// buildSummary is the wire shape returned to the React client. It's a
// thin projection of KusoBuild that exposes a stable `id` (the CR
// name) and pulls a few status fields out of the unstructured map so
// the frontend doesn't need to know about kube internals.
type buildSummary struct {
	ID              string `json:"id"`
	ServiceName     string `json:"serviceName"`
	Branch          string `json:"branch,omitempty"`
	CommitSha       string `json:"commitSha,omitempty"`
	CommitMessage   string `json:"commitMessage,omitempty"`
	ImageTag        string `json:"imageTag,omitempty"`
	Status          string `json:"status"`
	StartedAt       string `json:"startedAt,omitempty"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	TriggeredBy     string `json:"triggeredBy,omitempty"`     // user|webhook|api|system
	TriggeredByUser string `json:"triggeredByUser,omitempty"` // username for source=user
	// ErrorMessage is the extracted failure cause for status=failed
	// builds. archiveLogs.extractFailureReason regex-scans the build's
	// tail logs and stamps the hit (or the kubelet's terminated reason
	// — OOMKilled, exit code, eviction) onto kuso.sislelabs.com/build-
	// message. The Deployments-tab UI renders it as a sticky red banner
	// above the log viewer so users don't have to hand-grep 200-600
	// lines of kaniko noise to find "why".
	ErrorMessage string `json:"errorMessage,omitempty"`
}

func toBuildSummary(b kube.KusoBuild) buildSummary {
	out := buildSummary{
		ID:          b.Name,
		ServiceName: b.Spec.Service,
		Branch:      b.Spec.Branch,
		CommitSha:   b.Spec.Ref,
	}
	if b.Spec.Image != nil {
		out.ImageTag = b.Spec.Image.Tag
	}
	// Phase + timing live on annotations because helm-operator
	// rewrites .status on every reconcile. The legacy .status.phase
	// fallback covers CRs created before v0.6.3 — see builds.buildPhase
	// for the source of truth.
	if b.Annotations != nil {
		if v, ok := b.Annotations["kuso.sislelabs.com/build-phase"]; ok {
			out.Status = v
		}
		if v, ok := b.Annotations["kuso.sislelabs.com/build-started-at"]; ok {
			out.StartedAt = v
		}
		if v, ok := b.Annotations["kuso.sislelabs.com/build-completed-at"]; ok {
			out.FinishedAt = v
		}
		if v, ok := b.Annotations["kuso.sislelabs.com/build-triggered-by"]; ok {
			out.TriggeredBy = v
		}
		if v, ok := b.Annotations["kuso.sislelabs.com/build-triggered-by-user"]; ok {
			out.TriggeredByUser = v
		}
		if v, ok := b.Annotations["kuso.sislelabs.com/build-commit-message"]; ok {
			out.CommitMessage = v
		}
		// build-message is the extracted failure cause. Only meaningful
		// for terminal failed/cancelled phases; succeeded builds may
		// also stamp this (e.g. cancelled-then-cleaned) but the UI gates
		// on status=failed.
		if v, ok := b.Annotations["kuso.sislelabs.com/build-message"]; ok {
			out.ErrorMessage = v
		}
	}
	if out.Status == "" && b.Status != nil {
		if s, ok := b.Status["phase"].(string); ok {
			out.Status = s
		}
		if s, ok := b.Status["startedAt"].(string); ok {
			out.StartedAt = s
		}
		if s, ok := b.Status["finishedAt"].(string); ok {
			out.FinishedAt = s
		}
	}
	if out.Status == "" {
		out.Status = "pending"
	}
	// Fallback: a running build that hasn't had its build-started-at
	// annotation stamped yet (kaniko Job hasn't gone Active) still
	// has a CR creationTimestamp. Use that as the lower bound so the
	// deployments panel can render an elapsed timer instead of "—".
	//
	// Scoped to running ONLY — for queued/pending we deliberately
	// leave startedAt empty. A queued build that sat 10 minutes
	// behind another should not display "10m duration" because it
	// hasn't actually started any work yet.
	if out.StartedAt == "" && out.Status == "running" {
		if !b.CreationTimestamp.IsZero() {
			out.StartedAt = b.CreationTimestamp.UTC().Format(time.RFC3339)
		}
	}
	return out
}

// List returns the builds for a service, newest first.
func (h *BuildsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := buildsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleViewer) {
		return
	}
	raw, err := h.Svc.List(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
	if err != nil {
		h.fail(w, "list builds", err)
		return
	}
	out := make([]buildSummary, 0, len(raw))
	for _, b := range raw {
		out = append(out, toBuildSummary(b))
	}
	writeJSON(w, http.StatusOK, out)
}

// Create triggers a new build for the service. Body: {branch?, ref?}.
func (h *BuildsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req builds.CreateBuildRequest
	// Empty body is legitimate: caller wants a build of the default
	// branch with synthetic ref.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := buildsCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleEditor) {
		return
	}
	// Stamp trigger context: who clicked Redeploy. The github webhook
	// dispatcher fills its own (source=webhook); requests with no
	// auth claims (shouldn't happen post-requireProjectAccess but is
	// defensive) fall back to source=api.
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		req.TriggeredBy = "user"
		req.TriggeredByUser = claims.Username
	} else {
		req.TriggeredBy = "api"
	}
	out, err := h.Svc.Create(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), req)
	if err != nil {
		h.fail(w, "create build", err)
		return
	}
	writeJSON(w, http.StatusCreated, toBuildSummary(*out))
}

func (h *BuildsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, builds.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, builds.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, builds.ErrConflict):
		// Pass the conflict message through so the UI can show
		// "build already in flight" instead of a bare 409.
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		h.Logger.Error("builds handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}
