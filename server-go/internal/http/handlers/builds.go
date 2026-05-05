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
	raw, err := h.Svc.List(ctx, project, "")
	if err != nil {
		h.fail(w, "list project builds", err)
		return
	}
	// Newest-first ordering already enforced by Service.List, so the
	// first build we see for a given service IS the latest. Skip
	// duplicates.
	seen := map[string]bool{}
	out := map[string]buildSummary{}
	for _, b := range raw {
		// b.Spec.Service is the FQ name "<project>-<service>". The
		// canvas keys by short name, so strip the prefix.
		fq := b.Spec.Service
		short := fq
		if prefix := project + "-"; len(fq) > len(prefix) && fq[:len(prefix)] == prefix {
			short = fq[len(prefix):]
		}
		if seen[short] {
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
		return
	}
	out, err := h.Svc.Rollback(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "service"), chi.URLParam(r, "build"))
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
	// Fallback: a running/pending build that hasn't had its
	// build-started-at annotation stamped yet (kaniko Job hasn't gone
	// Active) still has a CR creationTimestamp. Use that as the lower
	// bound so the deployments panel can render an elapsed timer
	// instead of a blank "—". Finished builds keep their real timing.
	if out.StartedAt == "" && (out.Status == "running" || out.Status == "pending") {
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
	if !requireProjectAccess(ctx, w, h.DB, chi.URLParam(r, "project"), db.ProjectRoleDeployer) {
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
