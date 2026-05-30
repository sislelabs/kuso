package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// Revision endpoints. The CR-mutating endpoints (PatchService,
// SetEnv, etc.) call writeRevision after a successful kube write so
// the History tab can render a chronological list and Revert can
// replay the stored snapshot.
//
// Why these live in a separate file: they share the projects routes'
// chi mounting + 5s timeout, but the read/write/revert path doesn't
// need the projects service at all — only the DB. Keeping them out
// of projects.go makes the projects file shorter and the revision
// surface obviously self-contained.

// ListRevisions returns the most recent revisions for one CR.
// Optional ?limit=N caps the result; default 50, hard cap 200.
func (h *ProjectsHandler) ListRevisions(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeJSON(w, http.StatusOK, []db.Revision{})
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	kind := chi.URLParam(r, "kind")
	name := chi.URLParam(r, "name")
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	out, err := h.DB.ListRevisions(ctx, project, kind, name, limit)
	if err != nil {
		h.fail(w, "list revisions", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// GetRevision returns one revision by id (full snapshot included).
func (h *ProjectsHandler) GetRevision(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := projectCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	rev, err := h.DB.GetRevision(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, "get revision", err)
		return
	}
	// Project-scope gate. Without this, anyone with a valid JWT could
	// fetch any revision snapshot by ID — which includes the full
	// patched JSON of the resource, often containing env-var values
	// and other project-private state.
	if !requireProjectAccess(ctx, w, h.DB, rev.Project, db.ProjectRoleViewer) {
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

// RevertRevision replays the stored snapshot back through the
// matching update endpoint. Currently supports kind="service" by
// PATCHing the service spec; addon/environment revert returns 501
// for now (we can add them once the service path proves out).
//
// We don't auto-create a "revert revision" before applying — the
// PATCH itself triggers a fresh InsertRevision via the standard
// write path. So the History tab shows: original save → revert
// (which is itself a new revision) → user can revert that to roll
// forward again. No special-case state to keep in sync.
func (h *ProjectsHandler) RevertRevision(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		http.Error(w, "revisions disabled", http.StatusServiceUnavailable)
		return
	}
	// No JWT-perm pre-gate here: in role-system v2 services:write is a
	// per-project perm not present in any token, so a requirePerm check
	// would block everyone. The authoritative gate is the project-scoped
	// requireProjectAccess(...Editor) below, once we know the revision's
	// project. Revision IDs are opaque and we 404 on not-found, so
	// loading the revision before the gate doesn't leak.
	ctx, cancel := projectCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	rev, err := h.DB.GetRevision(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		h.fail(w, "get revision", err)
		return
	}
	// Project-scope gate. Pre-fix any caller with services:write could
	// revert any revision regardless of which project it belonged to —
	// effectively cross-project mutation. Gate on Deployer-or-higher
	// on the revision's project; 404 (not 403) so probing for revision
	// IDs doesn't leak existence.
	if !requireProjectAccess(ctx, w, h.DB, rev.Project, db.ProjectRoleEditor) {
		return
	}
	switch rev.Kind {
	case "service":
		if err := h.revertServiceFromSnapshot(ctx, rev); err != nil {
			h.fail(w, "revert service", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reverted", "kind": "service"})
	case "addon":
		if h.AddonReverter == nil {
			http.Error(w, "addon revert unavailable", http.StatusServiceUnavailable)
			return
		}
		var snap struct {
			Patch json.RawMessage `json:"patch"`
		}
		if err := json.Unmarshal(rev.Snapshot, &snap); err != nil {
			h.fail(w, "decode addon revision", err)
			return
		}
		if err := h.AddonReverter.RevertAddon(ctx, rev.Project, rev.Name, snap.Patch); err != nil {
			h.fail(w, "revert addon", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reverted", "kind": "addon"})
	default:
		http.Error(w, "revert: only kind=service and kind=addon are supported", http.StatusNotImplemented)
	}
}

// revertServiceFromSnapshot decodes the snapshot into a PatchService
// request and applies it. We re-use the existing patch path so any
// validation / propagation / notification it does runs on revert too.
func (h *ProjectsHandler) revertServiceFromSnapshot(ctx context.Context, rev *db.Revision) error {
	var snap struct {
		Patch json.RawMessage `json:"patch"`
	}
	if err := json.Unmarshal(rev.Snapshot, &snap); err != nil {
		return err
	}
	// Forward the raw snapshot.patch payload through the same
	// PatchService implementation. We don't go through HTTP again —
	// the service has a public method that takes a parsed body.
	return h.Svc.RevertService(ctx, rev.Project, rev.Name, snap.Patch)
}
