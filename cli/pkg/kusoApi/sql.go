// SQL browser (read/query) API client for the addon SQL surface.
//
// These mirror the server's /api/projects/{project}/addons/{addon}/sql/*
// endpoints (server-go/internal/http/handlers/sql_data.go + backups.go).
// Only the READ + arbitrary-query paths are wired here; the destructive
// row-mutation endpoints (POST/PATCH/DELETE .../sql/rows) are the
// grid-editor ops and are deliberately out of scope for the CLI.
//
// All of these are admin-gated server-side (same secret-bearing-read
// boundary as env values / shell) — the caller gets a 403 otherwise.

package kusoApi

import (
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

// SQLTables lists the addon database's user tables (pg_catalog +
// information_schema filtered out). Response is a JSON array of
// {"schema": ..., "name": ...}.
func (k *KusoClient) SQLTables(project, addon string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/sql/tables")
}

// SQLColumns introspects one table's columns + primary key + enum
// labels. schema/table are QUERY params (the server reads them off the
// URL query, not the path). Response is columnsResponse:
// {"columns": [...], "primaryKey": [...], "editable": bool}.
func (k *KusoClient) SQLColumns(project, addon, schema, table string) (*resty.Response, error) {
	q := url.Values{}
	if schema != "" {
		q.Set("schema", schema)
	}
	q.Set("table", table)
	return k.client.Get("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/sql/columns?" + q.Encode())
}

// SQLRows returns a paginated single-table SELECT. schema/table/limit/
// offset are QUERY params. Response is rowsResponse:
// {"columns": [...], "rows": [[...]], "nulls": [[...]], "total": N,
//  "truncated": bool, "elapsed": "..."}.
func (k *KusoClient) SQLRows(project, addon, schema, table string, limit, offset int) (*resty.Response, error) {
	q := url.Values{}
	if schema != "" {
		q.Set("schema", schema)
	}
	q.Set("table", table)
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	return k.client.Get("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/sql/rows?" + q.Encode())
}

// SQLQueryRequest is the POST body for the raw read-only query runner.
// Mirrors the server's handlers.SQLQueryRequest. The query itself is
// enforced read-only server-side inside a read-only tx; Limit caps the
// returned rows (server default 100, hard cap 1000).
type SQLQueryRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// SQLQuery runs an arbitrary read-only SELECT against the addon.
// project/addon are escaped as path segments; the query text rides in
// the JSON body and is NOT path-escaped. Response is SQLQueryResponse:
// {"columns": [...], "rows": [[...]], "truncated": bool, "elapsed": "..."}.
func (k *KusoClient) SQLQuery(project, addon string, req SQLQueryRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/projects/" + esc(project) + "/addons/" + esc(addon) + "/sql/query")
}
