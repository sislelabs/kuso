package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
)

// AuditHandler exposes the /api/audit endpoints.
type AuditHandler struct {
	Svc    *audit.Service
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *AuditHandler) Mount(r chi.Router) {
	r.Get("/api/audit", h.List)
	r.Get("/api/audit/app/{pipeline}/{phase}/{app}", h.ListForApp)
}

func auditCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// List splits on ?project=. With it, project Viewer is enough — a
// teammate should be able to see audit rows for projects they can
// already deploy to. Without it, the call asks for the cross-project
// (instance-wide) view, which stays admin-only.
//
// Pagination is keyset on id: ?limit=N (clamped 1–1000, default 100)
// plus ?after=<id> to fetch rows older than that id — both scopes
// support it. Truncation signal (agent-use W4): when the page was cut
// (older rows exist), the response carries
//
//	X-Kuso-Truncated: true
//	X-Kuso-Next-After: <id of the oldest returned row>
//
// pass that id as ?after= for the next page. The JSON envelope shape
// ({"audit","count","limit"}) is wire-stable and unchanged.
func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	project := r.URL.Query().Get("project")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ctx, cancel := auditCtx(r)
	defer cancel()

	if project == "" {
		if !requireAdmin(w, r) {
			return
		}
	} else {
		if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
			return
		}
	}

	var (
		rows  []audit.Entry
		count int
		more  bool
		err   error
	)
	if project != "" {
		rows, count, more, err = h.Svc.GetForProject(ctx, project, after, limit)
	} else {
		rows, count, more, err = h.Svc.Get(ctx, after, limit)
	}
	if err != nil {
		h.Logger.Error("list audit", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if rows == nil {
		rows = []audit.Entry{}
	}
	if more && len(rows) > 0 {
		setTruncationHeaders(w, headerNextAfter, strconv.FormatInt(rows[len(rows)-1].ID, 10))
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows, "count": count, "limit": effectiveLimit(limit)})
}

// ListForApp gates on Viewer of the {pipeline} project — pipeline is
// the v0.2 project label, so a project member viewing their own
// service's history is a normal flow. Sets the same X-Kuso-Truncated /
// X-Kuso-Next-After headers as List when the page was cut.
func (h *AuditHandler) ListForApp(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	pipeline := chi.URLParam(r, "pipeline")
	ctx, cancel := auditCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, pipeline, db.ProjectRoleViewer) {
		return
	}
	rows, count, more, err := h.Svc.GetForApp(ctx, pipeline, chi.URLParam(r, "phase"), chi.URLParam(r, "app"), limit)
	if err != nil {
		h.Logger.Error("list audit for app", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if rows == nil {
		rows = []audit.Entry{}
	}
	if more && len(rows) > 0 {
		setTruncationHeaders(w, headerNextAfter, strconv.FormatInt(rows[len(rows)-1].ID, 10))
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows, "count": count, "limit": effectiveLimit(limit)})
}

func effectiveLimit(in int) int {
	if in <= 0 || in > 1000 {
		return 100
	}
	return in
}
