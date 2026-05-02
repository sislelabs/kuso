package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
)

// AuditHandler exposes the /api/audit endpoints.
type AuditHandler struct {
	Svc    *audit.Service
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

func (h *AuditHandler) List(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	project := r.URL.Query().Get("project")
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	ctx, cancel := auditCtx(r)
	defer cancel()
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

func (h *AuditHandler) ListForApp(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	ctx, cancel := auditCtx(r)
	defer cancel()
	rows, count, err := h.Svc.GetForApp(ctx, chi.URLParam(r, "pipeline"), chi.URLParam(r, "phase"), chi.URLParam(r, "app"), limit)
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
