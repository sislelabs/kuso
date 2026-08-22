package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// ErrorsHandler exposes /api/projects/{project}/services/{service}/errors.
// Reads the ErrorEvent table populated by the errorscan goroutine and
// returns groups (one row per fingerprint).
type ErrorsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the route. JWT-protected — the caller's project
// access is verified against requireProjectAccess.
func (h *ErrorsHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/errors", h.List)
}

// List returns aggregated error groups. Query params:
//
//	?since=24h   — lookback window. Default 24h, max 30d.
//	?limit=50    — max groups returned (1–200, default 50).
//	?offset=0    — groups to skip (offset paging, newest-first order).
//
// Truncation signal (agent-use W4): the wire shape is a bare JSON
// array and MUST stay that way, so a cut response is signalled via
// headers instead — X-Kuso-Truncated: true plus X-Kuso-Next-Offset
// with the ?offset= value for the next page. Detection is exact (the
// DB read over-fetches one group past the limit), not a full-page
// guess. Absent headers mean the response is complete.
func (h *ErrorsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	project := chi.URLParam(r, "project")
	service := chi.URLParam(r, "service")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	// A bad `since` is rejected rather than silently defaulted. The old
	// code swallowed the parse error, so the UI's "7d"/"30d" options —
	// which time.ParseDuration cannot parse — quietly queried 24h while
	// the panel still rendered "0 error groups in last 7d".
	since := time.Now().Add(-24 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		d, err := parseRangeDuration(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "bad since")
			return
		}
		if d > 30*24*time.Hour {
			d = 30 * 24 * time.Hour
		}
		since = time.Now().Add(-d)
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}
	// +1 over-fetch: one extra group past the limit proves truncation.
	groups, err := h.DB.ListErrorGroups(ctx, project, service, since, limit+1, offset)
	if err != nil {
		h.Logger.Error("errors: list", "err", err, "project", project, "service", service)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	if len(groups) > limit {
		groups = groups[:limit]
		setTruncationHeaders(w, headerNextOffset, strconv.Itoa(offset+limit))
	}
	writeJSON(w, http.StatusOK, groups)
}
