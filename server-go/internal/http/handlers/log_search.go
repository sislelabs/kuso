// Log search endpoint. Backed by the LogLine table populated by the
// logship goroutine. FTS5 MATCH grammar — phrase with quotes,
// AND/OR/NOT, prefix (foo*), no implicit boolean (FTS5 default is
// AND-of-tokens). The search is scoped to a single (project, service).

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

type LogSearchHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

func (h *LogSearchHandler) Mount(r chi.Router) {
	r.Get("/api/projects/{project}/services/{service}/logs/search", h.Search)
	// Project-wide search — useful for "what crashed in this project
	// in the last hour" without paging through services.
	r.Get("/api/projects/{project}/logs/search", h.SearchProject)
}

func (h *LogSearchHandler) Search(w http.ResponseWriter, r *http.Request) {
	h.search(w, r, chi.URLParam(r, "project"), chi.URLParam(r, "service"))
}

func (h *LogSearchHandler) SearchProject(w http.ResponseWriter, r *http.Request) {
	h.search(w, r, chi.URLParam(r, "project"), "")
}

func (h *LogSearchHandler) search(w http.ResponseWriter, r *http.Request, project, service string) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	since := parseTs(q.Get("since"))
	until := parseTs(q.Get("until"))
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	rows, err := h.DB.SearchLogs(ctx, db.SearchLogsRequest{
		Project: project,
		Service: service,
		Env:     q.Get("env"),
		Query:   q.Get("q"),
		Since:   since,
		Until:   until,
		Limit:   limit,
	})
	if err != nil {
		// Don't leak FTS5 grammar errors back to the caller — those are
		// implementation details of the search engine and a probe vector
		// for an attacker who wants to fingerprint the server.
		h.Logger.Error("log search", "err", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": project,
		"service": service,
		"q":       q.Get("q"),
		"lines":   rows,
	})
}

// parseTs accepts RFC3339 or "1700000000" (unix). Returns zero on
// empty/garbage.
func parseTs(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0)
	}
	return time.Time{}
}
