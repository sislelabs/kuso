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
	"kuso/server/internal/kube"
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
	for i := range out {
		redactRevisionSnapshotIfNeeded(ctx, h.DB, project, &out[i])
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
	redactRevisionSnapshotIfNeeded(ctx, h.DB, rev.Project, rev)
	writeJSON(w, http.StatusOK, rev)
}

// redactRevisionSnapshotIfNeeded strips secret material from a revision
// snapshot for callers without secrets:read on the project. Snapshots
// are the RAW patch bodies ({"patch": <req>}) — they carry whatever the
// mutating request carried: token-bearing repo URLs, plaintext env-var
// values, addon passwords. The live handlers mask/redact all of those;
// without this the History tab was a viewer-readable side door around
// every one of the read gates. Revert is unaffected — it replays the
// STORED row server-side and never echoes it.
func redactRevisionSnapshotIfNeeded(ctx context.Context, dbConn *db.DB, project string, rev *db.Revision) {
	if rev == nil || len(rev.Snapshot) == 0 || callerCanReadSecrets(ctx, dbConn, project) {
		return
	}
	var v any
	if err := json.Unmarshal(rev.Snapshot, &v); err != nil {
		// Unparseable snapshot: fail closed — an empty object beats
		// echoing bytes we couldn't inspect.
		rev.Snapshot = json.RawMessage(`{}`)
		return
	}
	b, err := json.Marshal(redactSnapshotValue(v))
	if err != nil {
		rev.Snapshot = json.RawMessage(`{}`)
		return
	}
	rev.Snapshot = b
}

// redactSnapshotValue walks arbitrary snapshot JSON and scrubs the
// secret shapes patch bodies can carry: env-var literals
// ({"name": …, "value": …} pairs), addon "password" fields, repo
// "token" fields (GitLab clone credentials — old rows persisted them
// verbatim before the write path learned to drop them), and
// credential-bearing repo URLs in any string. Shape-based rather than
// schema-based so it holds for service, addon, and future kinds.
func redactSnapshotValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		_, hasName := t["name"].(string)
		for k, val := range t {
			// buildArgs / buildEnv are KEY→VALUE maps whose values are
			// conventionally build-time credentials (NPM_TOKEN, private
			// registry auth, SENTRY_AUTH_TOKEN). The per-key rules below
			// can't catch them: the keys are user-chosen, so nothing is
			// literally named "token". Mask every value, keep every key
			// so History still shows WHICH args changed.
			//
			// The write path now masks buildArgs before persisting, but
			// this read-side pass still matters for rows written before
			// that fix — they hold plaintext on disk today.
			if k == "buildArgs" || k == "buildEnv" {
				if m, ok := val.(map[string]any); ok {
					for mk := range m {
						m[mk] = envMaskSentinel
					}
					t[k] = m
					continue
				}
			}
			if s, ok := val.(string); ok && s != "" {
				switch {
				case k == "password" || k == "token":
					t[k] = envMaskSentinel
					continue
				case k == "value" && hasName:
					t[k] = envMaskSentinel
					continue
				}
				if kube.RepoURLHasCredentials(s) {
					t[k] = kube.StripRepoURLCredentials(s)
					continue
				}
				continue
			}
			t[k] = redactSnapshotValue(val)
		}
		return t
	case []any:
		for i := range t {
			t[i] = redactSnapshotValue(t[i])
		}
		return t
	case string:
		if kube.RepoURLHasCredentials(t) {
			return kube.StripRepoURLCredentials(t)
		}
		return t
	default:
		return v
	}
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
