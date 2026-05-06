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
		err   error
	)
	if project != "" {
		rows, count, err = h.Svc.GetForProject(ctx, project, after, limit)
	} else {
		rows, count, err = h.Svc.Get(ctx, limit)
	}
	if err != nil {
		h.Logger.Error("list audit", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []audit.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows, "count": count, "limit": effectiveLimit(limit)})
}

// ListForApp gates on Viewer of the {pipeline} project — pipeline is
// the v0.2 project label, so a project member viewing their own
// service's history is a normal flow.
func (h *AuditHandler) ListForApp(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	pipeline := chi.URLParam(r, "pipeline")
	ctx, cancel := auditCtx(r)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, pipeline, db.ProjectRoleViewer) {
		return
	}
	rows, count, err := h.Svc.GetForApp(ctx, pipeline, chi.URLParam(r, "phase"), chi.URLParam(r, "app"), limit)
	if err != nil {
		h.Logger.Error("list audit for app", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []audit.Entry{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": rows, "count": count, "limit": effectiveLimit(limit)})
}

func effectiveLimit(in int) int {
	if in <= 0 || in > 1000 {
		return 100
	}
	return in
}
