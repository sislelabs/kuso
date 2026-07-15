package handlers

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
)

// SQLDatabases lists the logical databases on a postgres addon's server
// (templates excluded). Multi-DB addons — e.g. a platform hosting one
// database per tenant inside a single shared Postgres — get a database
// picker in the SQL browser backed by this; every other /sql endpoint
// accepts the picked name via ?database=.
func (h *BackupsHandler) SQLDatabases(w http.ResponseWriter, r *http.Request) {
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		http.Error(w, "forbidden: the SQL browser requires the admin role", http.StatusForbidden)
		return
	}
	conn, err := h.pgConn(ctx, project, addon, "")
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx,
		`SELECT datname FROM pg_database WHERE NOT datistemplate AND datallowconn ORDER BY datname`)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		names = append(names, n)
	}
	writeJSON(w, http.StatusOK, map[string]any{"databases": names})
}
