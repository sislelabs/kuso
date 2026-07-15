package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/lib/pq"

	"kuso/server/internal/audit"
	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// sql_data.go — the structured table data browser/editor on the addon
// SQL surface. Unlike the raw /sql/query runner (read-only tx, arbitrary
// SELECT), these endpoints build PARAMETERIZED statements from validated
// identifiers + bound values, so they're the only sanctioned write path.
//
// The statement builders here are PURE (no kube, no DB) so the security-
// critical bits — identifier validation, PK-targeting, NULL binding — are
// unit-testable without a live Postgres. See sql_data_test.go.

// cellValue is the wire shape for a single value crossing the boundary in
// a write request. IsNull distinguishes SQL NULL from "" / 0 / false; the
// builders bind NULL when IsNull, else the raw Value (Postgres does final
// type coercion via the column type — we never re-parse pg types in Go).
type cellValue struct {
	Value  any  `json:"value"`
	IsNull bool `json:"isNull"`
}

// bind returns the value to pass to database/sql for this cell.
func (c cellValue) bind() any {
	if c.IsNull {
		return nil
	}
	return c.Value
}

// colSet is the validated column universe for one table: the set of real
// column names (lower-cased compare is NOT done — Postgres identifiers are
// case-sensitive once quoted, so we match exactly) plus the primary key.
type colSet struct {
	cols map[string]bool // column name -> exists
	pk   []string        // primary-key column names, in key order
}

func newColSet(columns []string, pk []string) colSet {
	m := make(map[string]bool, len(columns))
	for _, c := range columns {
		m[c] = true
	}
	return colSet{cols: m, pk: pk}
}

func (cs colSet) has(col string) bool { return cs.cols[col] }

func (cs colSet) hasPK() bool { return len(cs.pk) > 0 }

// pkComplete reports whether the provided key map covers EXACTLY the
// primary-key columns (no missing PK col, no extra non-PK col). This is
// what guarantees an UPDATE/DELETE targets one row by its identity.
func (cs colSet) pkComplete(key map[string]cellValue) bool {
	if !cs.hasPK() || len(key) != len(cs.pk) {
		return false
	}
	for _, c := range cs.pk {
		if _, ok := key[c]; !ok {
			return false
		}
	}
	return true
}

// qualifiedName quotes schema.table for safe interpolation. Caller MUST
// have validated that the schema/table exist first.
func qualifiedName(schema, table string) string {
	return pq.QuoteIdentifier(schema) + "." + pq.QuoteIdentifier(table)
}

// validOrderDir normalizes a sort direction to ASC/DESC (default ASC).
func validOrderDir(dir string) string {
	if strings.EqualFold(strings.TrimSpace(dir), "desc") {
		return "DESC"
	}
	return "ASC"
}

// buildSelect renders a paginated single-table SELECT. orderBy "" means no
// ORDER BY. orderBy, when set, MUST be a real column (caller validates via
// colSet.has). limit/offset bind as $1/$2. Returns the SQL + args.
func buildSelect(schema, table, orderBy, dir string, limit, offset int) (string, []any) {
	var b strings.Builder
	b.WriteString("SELECT * FROM ")
	b.WriteString(qualifiedName(schema, table))
	if orderBy != "" {
		b.WriteString(" ORDER BY ")
		b.WriteString(pq.QuoteIdentifier(orderBy))
		b.WriteString(" ")
		b.WriteString(validOrderDir(dir))
	}
	b.WriteString(" LIMIT $1 OFFSET $2")
	return b.String(), []any{limit, offset}
}

// buildCount renders the row-count for pagination.
func buildCount(schema, table string) string {
	return "SELECT count(*) FROM " + qualifiedName(schema, table)
}

// buildInsert renders a parameterized INSERT … RETURNING * from a
// validated value map. Columns are sorted for deterministic output (the
// test relies on it; Postgres doesn't care about column order). Returns
// an error if no columns were provided.
func buildInsert(schema, table string, values map[string]cellValue) (string, []any, error) {
	if len(values) == 0 {
		return "", nil, fmt.Errorf("insert: no columns provided")
	}
	cols := sortedKeys(values)
	placeholders := make([]string, len(cols))
	args := make([]any, len(cols))
	for i, c := range cols {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = values[c].bind()
		cols[i] = pq.QuoteIdentifier(c)
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING *",
		qualifiedName(schema, table),
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "))
	return q, args, nil
}

// buildUpdate renders a parameterized UPDATE … WHERE <pk> RETURNING * from
// validated set + key maps. SET params come first ($1..$n), then the PK
// params ($n+1..). Returns an error if set is empty.
func buildUpdate(schema, table string, set, key map[string]cellValue) (string, []any, error) {
	if len(set) == 0 {
		return "", nil, fmt.Errorf("update: no columns to set")
	}
	setCols := sortedKeys(set)
	keyCols := sortedKeys(key)

	args := make([]any, 0, len(setCols)+len(keyCols))
	assignments := make([]string, len(setCols))
	n := 0
	for i, c := range setCols {
		n++
		assignments[i] = fmt.Sprintf("%s = $%d", pq.QuoteIdentifier(c), n)
		args = append(args, set[c].bind())
	}
	wheres := make([]string, len(keyCols))
	for i, c := range keyCols {
		n++
		wheres[i] = fmt.Sprintf("%s = $%d", pq.QuoteIdentifier(c), n)
		args = append(args, key[c].bind())
	}
	q := fmt.Sprintf("UPDATE %s SET %s WHERE %s RETURNING *",
		qualifiedName(schema, table),
		strings.Join(assignments, ", "),
		strings.Join(wheres, " AND "))
	return q, args, nil
}

// buildDelete renders a parameterized DELETE … WHERE <pk> from a validated
// key map. Returns an error if key is empty (we never emit an unbounded
// DELETE).
func buildDelete(schema, table string, key map[string]cellValue) (string, []any, error) {
	if len(key) == 0 {
		return "", nil, fmt.Errorf("delete: no key provided")
	}
	keyCols := sortedKeys(key)
	args := make([]any, len(keyCols))
	wheres := make([]string, len(keyCols))
	for i, c := range keyCols {
		wheres[i] = fmt.Sprintf("%s = $%d", pq.QuoteIdentifier(c), i+1)
		args[i] = key[c].bind()
	}
	q := fmt.Sprintf("DELETE FROM %s WHERE %s",
		qualifiedName(schema, table),
		strings.Join(wheres, " AND "))
	return q, args, nil
}

// validateWriteIdentifiers checks that every column referenced in a write
// (the union of the provided maps) is a real column of the table. Returns
// the first offending column name, or "" when all are valid.
func validateWriteIdentifiers(cs colSet, maps ...map[string]cellValue) string {
	for _, m := range maps {
		for col := range m {
			if !cs.has(col) {
				return col
			}
		}
	}
	return ""
}

// sortedKeys returns the map keys sorted, for deterministic statement
// rendering (placeholder order must match arg order, and tests assert on
// the exact string).
func sortedKeys(m map[string]cellValue) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Introspection + HTTP handlers for the structured data browser/editor.
// These hang off BackupsHandler (same struct that owns the SQL surface) and
// reuse pgConn + the sql:read admin gate + audit logging from backups.go.
// ---------------------------------------------------------------------------

// columnInfo describes one column for type-aware editing in the grid.
type columnInfo struct {
	Name       string   `json:"name"`
	DataType   string   `json:"dataType"` // information_schema.data_type
	UDTName    string   `json:"udtName"`  // underlying type (e.g. enum type name)
	Nullable   bool     `json:"nullable"`
	Default    string   `json:"default,omitempty"`
	Ordinal    int      `json:"ordinal"`
	IsEnum     bool     `json:"isEnum"`
	EnumValues []string `json:"enumValues,omitempty"`
}

// columnsResponse is GET /sql/columns.
type columnsResponse struct {
	Columns    []columnInfo `json:"columns"`
	PrimaryKey []string     `json:"primaryKey"`
	Editable   bool         `json:"editable"` // false when the table has no PK
}

// loadColumns introspects a table's columns + primary key + enum labels.
// schema/table must already be confirmed to exist (the query simply returns
// no columns for a bogus table, which the handler treats as 404).
func loadColumns(ctx context.Context, conn *sql.DB, schema, table string) (columnsResponse, error) {
	var out columnsResponse
	colRows, err := conn.QueryContext(ctx, `
		SELECT column_name, data_type, udt_name, is_nullable, COALESCE(column_default, ''), ordinal_position
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
	`, schema, table)
	if err != nil {
		return out, err
	}
	// Drain the column rows into a slice and CLOSE the cursor BEFORE running
	// any further query. pgConn caps the pool at MaxOpenConns(1), so a nested
	// query issued while colRows is still open (the per-column enum lookup,
	// or the PK query below) can't acquire a connection — it fails, the error
	// gets swallowed, and the table comes back with no enum metadata AND no
	// primary key (wrongly flagged "read-only / no primary key"). Any table
	// with a USER-DEFINED enum column hit this. Reading into memory first
	// frees the single connection for the enum + PK queries that follow.
	for colRows.Next() {
		var c columnInfo
		var nullable string
		if err := colRows.Scan(&c.Name, &c.DataType, &c.UDTName, &nullable, &c.Default, &c.Ordinal); err != nil {
			continue
		}
		c.Nullable = nullable == "YES"
		out.Columns = append(out.Columns, c)
	}
	colRows.Close()

	// Now the connection is free: resolve enum labels for any USER-DEFINED
	// columns (an enum dropdown in the editor). Each is a separate query on
	// the now-idle single connection.
	for i := range out.Columns {
		if out.Columns[i].DataType != "USER-DEFINED" {
			continue
		}
		if vals, ok := loadEnumValues(ctx, conn, out.Columns[i].UDTName); ok {
			out.Columns[i].IsEnum = true
			out.Columns[i].EnumValues = vals
		}
	}

	// Resolve the PK columns via pg_catalog joins with parameterized
	// nspname/relname — NOT a `(schema||'.'||table)::regclass` cast, which
	// would bind the schema/table as a text literal and resolve the wrong
	// relation (and drags identifier text into a name-resolution context).
	pkRows, err := conn.QueryContext(ctx, `
		SELECT a.attname
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE n.nspname = $1 AND c.relname = $2 AND i.indisprimary
		ORDER BY array_position(i.indkey, a.attnum)
	`, schema, table)
	if err == nil {
		defer pkRows.Close()
		for pkRows.Next() {
			var col string
			if err := pkRows.Scan(&col); err == nil {
				out.PrimaryKey = append(out.PrimaryKey, col)
			}
		}
	} else {
		// A failed PK query silently flips a table to read-only ("no primary
		// key") even when it has one — exactly the bug that masked the
		// MaxOpenConns(1) interleaving. Never swallow it silently again: log
		// so the next regression is visible in server logs, not just the UI.
		slog.Default().Warn("sql browser: primary-key lookup failed; table will show as read-only",
			"schema", schema, "table", table, "err", err)
	}
	out.Editable = len(out.PrimaryKey) > 0
	return out, nil
}

// loadEnumValues returns the labels of a pg enum type, ordered.
func loadEnumValues(ctx context.Context, conn *sql.DB, typeName string) ([]string, bool) {
	rows, err := conn.QueryContext(ctx, `
		SELECT e.enumlabel
		FROM pg_type t JOIN pg_enum e ON e.enumtypid = t.oid
		WHERE t.typname = $1
		ORDER BY e.enumsortorder
	`, typeName)
	if err != nil {
		return nil, false
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	return out, len(out) > 0
}

// tableExists confirms a base table by that schema+name is present. Used to
// turn user-supplied schema/table into a validated identifier before any
// statement is built around it.
func tableExists(ctx context.Context, conn *sql.DB, schema, table string) (bool, error) {
	var n int
	err := conn.QueryRowContext(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = $1 AND table_name = $2 AND table_type = 'BASE TABLE'
	`, schema, table).Scan(&n)
	return n > 0, err
}

// sqlBrowserGate is the shared access check for every SQL-browser endpoint:
// project membership + the admin-only SQL role. Writes the HTTP error and
// returns false on denial.
func (h *BackupsHandler) sqlBrowserGate(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return false
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		http.Error(w, "forbidden: the data browser requires the admin role", http.StatusForbidden)
		return false
	}
	return true
}

// dataGuard runs the shared preamble for every data endpoint: admin gate,
// open a pg conn, confirm the table exists, and load its column set. On any
// failure it writes the HTTP error and returns ok=false. The caller owns
// closing conn when ok.
func (h *BackupsHandler) dataGuard(w http.ResponseWriter, r *http.Request, schema, table string) (conn *sql.DB, cs colSet, ok bool) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return nil, colSet{}, false
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		http.Error(w, "forbidden: the data browser requires the admin role", http.StatusForbidden)
		return nil, colSet{}, false
	}
	if schema == "" || table == "" {
		http.Error(w, "schema and table required", http.StatusBadRequest)
		return nil, colSet{}, false
	}
	conn, err := h.pgConn(ctx, project, addon, r.URL.Query().Get("database"))
	if err != nil {
		writeAddonErr(w, err)
		return nil, colSet{}, false
	}
	exists, err := tableExists(ctx, conn, schema, table)
	if err != nil {
		conn.Close()
		http.Error(w, "introspect: "+err.Error(), http.StatusBadGateway)
		return nil, colSet{}, false
	}
	if !exists {
		conn.Close()
		http.Error(w, "no such table", http.StatusNotFound)
		return nil, colSet{}, false
	}
	cols, err := loadColumns(ctx, conn, schema, table)
	if err != nil {
		conn.Close()
		http.Error(w, "columns: "+err.Error(), http.StatusBadGateway)
		return nil, colSet{}, false
	}
	names := make([]string, len(cols.Columns))
	for i, c := range cols.Columns {
		names[i] = c.Name
	}
	return conn, newColSet(names, cols.PrimaryKey), true
}

// SQLColumns — GET /sql/columns?schema=&table=
func (h *BackupsHandler) SQLColumns(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	schema := r.URL.Query().Get("schema")
	table := r.URL.Query().Get("table")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	if !callerCanRunSQL(ctx, h.DB, project) {
		http.Error(w, "forbidden: the data browser requires the admin role", http.StatusForbidden)
		return
	}
	if schema == "" || table == "" {
		http.Error(w, "schema and table required", http.StatusBadRequest)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	// ClickHouse: introspect via system.columns instead of the pg path.
	if info, isCH, cerr := h.clickhouseConnInfo(cctx, project, addon); cerr == nil && isCH {
		if exists, _ := h.clickhouseTableExists(cctx, info, schema, table); !exists {
			http.Error(w, "no such table", http.StatusNotFound)
			return
		}
		resp, err := h.clickhouseColumns(cctx, info, schema, table)
		if err != nil {
			http.Error(w, "columns: "+err.Error(), http.StatusBadGateway)
			return
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	conn, err := h.pgConn(cctx, project, addon, r.URL.Query().Get("database"))
	if err != nil {
		writeAddonErr(w, err)
		return
	}
	defer conn.Close()
	if exists, _ := tableExists(cctx, conn, schema, table); !exists {
		http.Error(w, "no such table", http.StatusNotFound)
		return
	}
	resp, err := loadColumns(cctx, conn, schema, table)
	if err != nil {
		http.Error(w, "columns: "+err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// rowsResponse is GET /sql/rows.
type rowsResponse struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Nulls     [][]bool   `json:"nulls"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
	Elapsed   string     `json:"elapsed"`
}

// SQLRows — GET /sql/rows?schema=&table=&limit=&offset=&orderBy=&dir=
func (h *BackupsHandler) SQLRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	schema, table := q.Get("schema"), q.Get("table")
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	// ClickHouse: read-only paginated rows over the HTTP interface. Run the
	// admin gate FIRST (before the conn-secret fetch) so an unauthorized caller
	// never triggers a K8s secret read. The pg path gates first too (dataGuard).
	if !h.sqlBrowserGate(cctx, w, r) {
		return
	}
	if info, isCH, cerr := h.clickhouseConnInfo(cctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon")); cerr == nil && isCH {
		if schema == "" || table == "" {
			http.Error(w, "schema and table required", http.StatusBadRequest)
			return
		}
		if exists, _ := h.clickhouseTableExists(cctx, info, schema, table); !exists {
			http.Error(w, "no such table", http.StatusNotFound)
			return
		}
		limit := parseIntDefault(q.Get("limit"), 100)
		if limit <= 0 || limit > 1000 {
			limit = 100
		}
		offset := parseIntDefault(q.Get("offset"), 0)
		if offset < 0 {
			offset = 0
		}
		orderBy := q.Get("orderBy")
		if orderBy != "" {
			cols, cerr2 := h.clickhouseColumns(cctx, info, schema, table)
			if cerr2 != nil {
				http.Error(w, "columns: "+cerr2.Error(), http.StatusBadGateway)
				return
			}
			known := false
			for _, c := range cols.Columns {
				if c.Name == orderBy {
					known = true
					break
				}
			}
			if !known {
				http.Error(w, "unknown orderBy column", http.StatusBadRequest)
				return
			}
		}
		start := time.Now()
		out, err := h.clickhouseRows(cctx, info, schema, table, orderBy, q.Get("dir"), limit, offset)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		out.Elapsed = time.Since(start).Round(time.Millisecond).String()
		writeJSON(w, http.StatusOK, out)
		return
	}

	// dataGuard uses r.Context(); swap in our timeout context for the work.
	r = r.WithContext(cctx)
	conn, cs, ok := h.dataGuard(w, r, schema, table)
	if !ok {
		return
	}
	defer conn.Close()

	limit := parseIntDefault(q.Get("limit"), 100)
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	offset := parseIntDefault(q.Get("offset"), 0)
	if offset < 0 {
		offset = 0
	}
	orderBy := q.Get("orderBy")
	if orderBy != "" && !cs.has(orderBy) {
		http.Error(w, "unknown orderBy column", http.StatusBadRequest)
		return
	}

	// Pin a single underlying connection so `SET statement_timeout` and the
	// SELECT that follows are guaranteed to run on the same session (a bare
	// *sql.DB may hand them to different pooled conns, dropping the timeout).
	pinned, err := conn.Conn(cctx)
	if err != nil {
		http.Error(w, "conn: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer pinned.Close()
	if _, err := pinned.ExecContext(cctx, "SET statement_timeout = '10s'"); err != nil {
		http.Error(w, "set timeout: "+err.Error(), http.StatusBadGateway)
		return
	}

	start := time.Now()
	selSQL, args := buildSelect(schema, table, orderBy, q.Get("dir"), limit+1, offset)
	rows, err := pinned.QueryContext(cctx, selSQL, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	out := rowsResponse{Columns: cols, Rows: [][]string{}, Nulls: [][]bool{}}
	for rows.Next() {
		if len(out.Rows) >= limit {
			out.Truncated = true
			break
		}
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		strs := make([]string, len(cols))
		nulls := make([]bool, len(cols))
		for i, v := range raw {
			if v == nil {
				nulls[i] = true
			}
			strs[i] = stringifyCell(v)
		}
		out.Rows = append(out.Rows, strs)
		out.Nulls = append(out.Nulls, nulls)
	}
	// Close the row cursor before reusing the pinned conn for the count
	// (the pinned *sql.Conn serializes statements — an open cursor would
	// block the next query on it).
	rows.Close()

	// Total count for pagination (best-effort; same pinned session, so it
	// inherits the statement_timeout set above).
	_ = pinned.QueryRowContext(cctx, buildCount(schema, table)).Scan(&out.Total)
	out.Elapsed = time.Since(start).Round(time.Millisecond).String()
	writeJSON(w, http.StatusOK, out)
}

// writeRowRequest is the body of POST/PATCH/DELETE /sql/rows.
type writeRowRequest struct {
	Schema string               `json:"schema"`
	Table  string               `json:"table"`
	Values map[string]cellValue `json:"values,omitempty"` // insert
	Set    map[string]cellValue `json:"set,omitempty"`    // update
	PK     map[string]cellValue `json:"pk,omitempty"`     // update/delete
}

// rejectIfClickHouseWrite returns true (and writes a 422) when the addon is a
// ClickHouse addon — the row editor's transactional single-row write model
// doesn't apply (UPDATE/DELETE are async ALTER mutations; INSERT bypasses the
// intended-pipeline ingestion). The data browser is read-only for ClickHouse.
func (h *BackupsHandler) rejectIfClickHouseWrite(ctx context.Context, w http.ResponseWriter, r *http.Request) bool {
	_, isCH, err := h.clickhouseConnInfo(ctx, chi.URLParam(r, "project"), chi.URLParam(r, "addon"))
	if err == nil && isCH {
		http.Error(w, "the data browser is read-only for ClickHouse addons (row edits map to async ALTER mutations, not transactional writes) — use the SQL query runner for DDL", http.StatusUnprocessableEntity)
		return true
	}
	return false
}

// SQLInsertRow — POST /sql/rows
func (h *BackupsHandler) SQLInsertRow(w http.ResponseWriter, r *http.Request) {
	var req writeRowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if h.rejectIfClickHouseWrite(cctx, w, r) {
		return
	}
	r = r.WithContext(cctx)
	conn, cs, ok := h.dataGuard(w, r, req.Schema, req.Table)
	if !ok {
		return
	}
	defer conn.Close()
	if bad := validateWriteIdentifiers(cs, req.Values); bad != "" {
		http.Error(w, "unknown column: "+bad, http.StatusBadRequest)
		return
	}
	q, args, err := buildInsert(req.Schema, req.Table, req.Values)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, cols, err := queryOneRow(cctx, conn, q, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if row == nil {
		// INSERT … RETURNING * returned nothing (e.g. an INSTEAD OF trigger
		// on a view swallowed it). The write may not have landed; don't
		// report a phantom success.
		http.Error(w, "insert returned no row", http.StatusUnprocessableEntity)
		return
	}
	h.auditWrite(cctx, r, "insert", req.Schema, req.Table, req.Values)
	writeJSON(w, http.StatusOK, map[string]any{"columns": cols, "row": row})
}

// SQLUpdateRow — PATCH /sql/rows
func (h *BackupsHandler) SQLUpdateRow(w http.ResponseWriter, r *http.Request) {
	var req writeRowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if h.rejectIfClickHouseWrite(cctx, w, r) {
		return
	}
	r = r.WithContext(cctx)
	conn, cs, ok := h.dataGuard(w, r, req.Schema, req.Table)
	if !ok {
		return
	}
	defer conn.Close()
	if !cs.pkComplete(req.PK) {
		http.Error(w, "update requires the table's full primary key (table may have no PK)", http.StatusUnprocessableEntity)
		return
	}
	if bad := validateWriteIdentifiers(cs, req.Set, req.PK); bad != "" {
		http.Error(w, "unknown column: "+bad, http.StatusBadRequest)
		return
	}
	q, args, err := buildUpdate(req.Schema, req.Table, req.Set, req.PK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, cols, err := queryOneRow(cctx, conn, q, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if row == nil {
		http.Error(w, "no row matched the primary key", http.StatusNotFound)
		return
	}
	h.auditWrite(cctx, r, "update", req.Schema, req.Table, req.PK)
	writeJSON(w, http.StatusOK, map[string]any{"columns": cols, "row": row})
}

// SQLDeleteRow — DELETE /sql/rows
func (h *BackupsHandler) SQLDeleteRow(w http.ResponseWriter, r *http.Request) {
	var req writeRowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if h.rejectIfClickHouseWrite(cctx, w, r) {
		return
	}
	r = r.WithContext(cctx)
	conn, cs, ok := h.dataGuard(w, r, req.Schema, req.Table)
	if !ok {
		return
	}
	defer conn.Close()
	if !cs.pkComplete(req.PK) {
		http.Error(w, "delete requires the table's full primary key (table may have no PK)", http.StatusUnprocessableEntity)
		return
	}
	if bad := validateWriteIdentifiers(cs, req.PK); bad != "" {
		http.Error(w, "unknown column: "+bad, http.StatusBadRequest)
		return
	}
	q, args, err := buildDelete(req.Schema, req.Table, req.PK)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := conn.ExecContext(cctx, q, args...)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		http.Error(w, "no row matched the primary key", http.StatusNotFound)
		return
	}
	if n > 1 {
		// Should be impossible (PK is unique) but never report success on a
		// multi-row delete — surfaces a schema surprise instead of silently
		// nuking rows.
		http.Error(w, "refusing: delete affected more than one row", http.StatusConflict)
		return
	}
	h.auditWrite(cctx, r, "delete", req.Schema, req.Table, req.PK)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// queryOneRow runs a RETURNING * statement and returns the single row as
// stringified cells + the column names. row is nil when nothing returned
// (e.g. an UPDATE that matched no PK).
func queryOneRow(ctx context.Context, conn *sql.DB, q string, args []any) (row []string, cols []string, err error) {
	rows, err := conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, _ = rows.Columns()
	if !rows.Next() {
		return nil, cols, nil
	}
	raw := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, cols, err
	}
	row = make([]string, len(cols))
	for i, v := range raw {
		row[i] = stringifyCell(v)
	}
	return row, cols, nil
}

// auditWrite logs a data mutation. Higher blast radius than a read, so every
// insert/update/delete leaves a trail with the targeting key.
func (h *BackupsHandler) auditWrite(ctx context.Context, r *http.Request, op, schema, table string, key map[string]cellValue) {
	if h.Audit == nil {
		return
	}
	project := chi.URLParam(r, "project")
	addon := chi.URLParam(r, "addon")
	uid := ""
	if c, ok := auth.ClaimsFromContext(ctx); ok && c != nil {
		uid = c.UserID
	}
	keyJSON, _ := json.Marshal(key)
	h.Audit.Log(ctx, audit.Entry{
		User:     uid,
		Severity: "warn",
		Action:   "addon.sql_write",
		Pipeline: project,
		App:      addon,
		Resource: "kusoaddon",
		Message:  fmt.Sprintf("%s %s.%s key=%s", op, schema, table, string(keyJSON)),
	})
}

// parseIntDefault parses a base-10 int, returning def on empty/overflow/
// malformed input (strconv.Atoi guards overflow, which a hand-rolled loop
// would not).
func parseIntDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
