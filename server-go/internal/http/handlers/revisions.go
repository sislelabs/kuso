package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/kube"
	"kuso/server/internal/projects"
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
	resolveRevisionActors(ctx, out, h.usernameForID)
	writeJSON(w, http.StatusOK, out)
}

// usernameForID maps a stored actor that is a user id to that user's
// username. Revisions recorded before actors were stored by name hold
// the opaque id.
func (h *ProjectsHandler) usernameForID(ctx context.Context, id string) (string, bool) {
	u, err := h.DB.FindUserByID(ctx, id)
	if err != nil || u == nil || u.Username == "" {
		return "", false
	}
	return u.Username, true
}

// resolveRevisionActors rewrites each actor that lookup recognises as a
// user id to the username, looking each distinct actor up once. Actors
// it doesn't recognise (already a username, deleted user) are kept.
func resolveRevisionActors(ctx context.Context, revs []db.Revision, lookup func(context.Context, string) (string, bool)) {
	seen := map[string]string{}
	for i := range revs {
		a := revs[i].Actor
		if a == "" {
			continue
		}
		name, done := seen[a]
		if !done {
			name = a
			if u, ok := lookup(ctx, a); ok {
				name = u
			}
			seen[a] = name
		}
		revs[i].Actor = name
	}
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
	if name, ok := h.usernameForID(ctx, rev.Actor); ok {
		rev.Actor = name
	}
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
// matching update path (service, environment, addon). Informational
// revisions — ones that record a change without replayable state —
// get a 422 naming why.
//
// We don't auto-create a "revert revision" before applying — the
// PATCH itself triggers a fresh InsertRevision via the standard
// write path. So the History tab shows: original save → revert
// (which is itself a new revision) → user can revert that to roll
// forward again. No special-case state to keep in sync.
func (h *ProjectsHandler) RevertRevision(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		writeErr(w, http.StatusServiceUnavailable, "revisions disabled")
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
	kind, err := h.replayRevision(ctx, rev)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]string{"status": "reverted", "kind": kind})
	case errors.Is(err, projects.ErrNotRevertable):
		writeErr(w, http.StatusUnprocessableEntity, fmt.Sprintf("revision %s (%s) can't be reverted: %s", rev.ID, rev.Summary, err.Error()))
	case errors.Is(err, errRevertUnsupported), errors.Is(err, errRevertUnavailable):
		status := http.StatusNotImplemented
		if errors.Is(err, errRevertUnavailable) {
			status = http.StatusServiceUnavailable
		}
		writeErr(w, status, err.Error())
	default:
		h.fail(w, "revert "+rev.Kind, err)
	}
}

var (
	errRevertUnsupported = errors.New("revert not supported for this kind")
	errRevertUnavailable = errors.New("revert unavailable")
)

// replayRevision dispatches a stored snapshot to the mutator that can replay
// it. Informational snapshots (secret writes, env creation, renames, every
// cron revision) are refused up front with ErrNotRevertable rather than
// silently no-oping.
func (h *ProjectsHandler) replayRevision(ctx context.Context, rev *db.Revision) (string, error) {
	if projects.RevisionInformational(rev.Snapshot) {
		return rev.Kind, fmt.Errorf("%w: it records what changed but stores no state to replay", projects.ErrNotRevertable)
	}
	switch rev.Kind {
	case "service":
		return "service", h.Svc.RevertServiceSnapshot(ctx, rev.Project, rev.Name, rev.Snapshot)
	case "environment":
		return "environment", h.Svc.RevertEnvironmentSnapshot(ctx, rev.Project, rev.Name, rev.Snapshot)
	case "addon":
		if h.AddonReverter == nil {
			return "addon", fmt.Errorf("%w: addon revert", errRevertUnavailable)
		}
		var snap struct {
			Patch json.RawMessage `json:"patch"`
		}
		if err := json.Unmarshal(rev.Snapshot, &snap); err != nil {
			return "addon", fmt.Errorf("decode addon revision: %w", err)
		}
		return "addon", h.AddonReverter.RevertAddon(ctx, rev.Project, rev.Name, snap.Patch)
	default:
		return rev.Kind, fmt.Errorf("%w: kind=%s", errRevertUnsupported, rev.Kind)
	}
}
