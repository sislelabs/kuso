package handlers

// ClickHouse support for the read-only SQL query runner (POST /sql/query).
//
// The Postgres path (pgConn + database/sql + a read-only tx) doesn't apply to
// ClickHouse: there's no information_schema-style read-only transaction and the
// dangerous builtins differ. Instead we run the query over ClickHouse's HTTP
// interface (8123) — the same interface `kuso db connect` tunnels — with:
//   - readonly=2 (allow SELECT + change settings, forbid all writes/DDL),
//   - a hard max_execution_time, and
//   - a builtin denylist for the file()/url()/remote()/*(...) table functions
//     that would let a SELECT read the filesystem or reach the network.
//
// Only the raw query runner is wired for ClickHouse. The row/column data
// browser (SQLRows/SQLColumns/…) stays Postgres-only — it's built on
// pg_catalog/information_schema introspection that has no clean CH analogue.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/addons"
)

// chConnInfo is the HTTP connection detail for a ClickHouse addon, read from
// its <release>-conn secret.
type chConnInfo struct {
	baseURL  string // http://host:port
	user     string
	password string
	database string
}

// clickhouseConnInfo returns (info, true) if the addon's conn secret looks like
// a ClickHouse addon (has CLICKHOUSE_HOST), else (_, false). Mirrors how the
// CLI's localDSNFromSecret discriminates by which canonical key is present.
func (h *BackupsHandler) clickhouseConnInfo(ctx context.Context, project, addon string) (chConnInfo, bool, error) {
	// Ownership-checked resolution + per-project execution namespace —
	// same rationale as pgConn (see ownedAddon).
	cr, ns, err := h.ownedAddon(ctx, project, addon)
	if err != nil {
		return chConnInfo{}, false, err
	}
	connSecret := addons.ConnSecretName(cr.Name)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(ns).Get(ctx, connSecret, metav1.GetOptions{})
	if err != nil {
		return chConnInfo{}, false, err
	}
	host := string(sec.Data["CLICKHOUSE_HOST"])
	if host == "" {
		return chConnInfo{}, false, nil // not a clickhouse addon
	}
	port := string(sec.Data["CLICKHOUSE_HTTP_PORT"])
	if port == "" {
		port = "8123"
	}
	info := chConnInfo{
		baseURL:  fmt.Sprintf("http://%s:%s", host, port),
		user:     valueOr(string(sec.Data["CLICKHOUSE_USER"]), "default"),
		password: string(sec.Data["CLICKHOUSE_PASSWORD"]),
		database: string(sec.Data["CLICKHOUSE_DATABASE"]),
	}
	return info, true, nil
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// chCommentRe strips SQL comments (block and line) so they can't be used to
// break up a function name we're scanning for (fi/**/le is a CH syntax error,
// but stripping comments first is cheap and removes the whole class).
var chCommentRe = regexp.MustCompile(`(?s)/\*.*?\*/|--[^\n]*`)

// chFuncCallRe matches a bareword identifier immediately followed by optional
// whitespace and an open paren — i.e. a function call. ClickHouse accepts
// `file ('x')` (whitespace before the paren), so a literal "file(" substring
// scan is evadable; we normalize by extracting the function-name-before-paren.
var chFuncCallRe = regexp.MustCompile(`([a-z0-9_]+)\s*\(`)

// blockedFunctions maps a lowercased ClickHouse function name to the reason it
// is denied. These reach the filesystem or the network (SSRF / local file
// read), which readonly mode does NOT block (readonly only stops writes).
var blockedFunctions = map[string]string{
	"file":               "filesystem access (file table function)",
	"url":                "outbound network (url table function)",
	"urlcluster":         "outbound network (urlCluster table function)",
	"remote":             "outbound network (remote table function)",
	"remotesecure":       "outbound network (remoteSecure table function)",
	"cluster":            "outbound network (cluster table function)",
	"clusterallreplicas": "outbound network (clusterAllReplicas table function)",
	"mysql":              "outbound network (mysql table function)",
	"postgresql":         "outbound network (postgresql table function)",
	"mongodb":            "outbound network (mongodb table function)",
	"redis":              "outbound network (redis table function)",
	"jdbc":               "outbound network (jdbc table function)",
	"odbc":               "outbound network (odbc table function)",
	"s3":                 "outbound network (s3 table function)",
	"s3cluster":          "outbound network (s3Cluster table function)",
	"hdfs":               "outbound network (hdfs table function)",
	"hdfscluster":        "outbound network (hdfsCluster table function)",
	"azureblobstorage":   "outbound network (azureBlobStorage table function)",
	"deltalake":          "outbound network (deltaLake table function)",
	"iceberg":            "outbound network (iceberg table function)",
	"input":              "input() table function",
}

// chSettingsRe matches a bareword SETTINGS keyword (case-insensitive) — the
// query-level settings clause. Used to reject settings overrides that could
// escape the subquery wrap and raise resource caps under readonly=2.
var chSettingsRe = regexp.MustCompile(`(?i)\bsettings\b`)

// blockedClickHouseClause rejects query-level clauses the read browser must not
// allow. Today that's a SETTINGS clause: under readonly=2 the user can raise
// settings other than readonly, and combined with the subquery LIMIT wrap a
// crafted `…) SETTINGS max_execution_time=0 --` would remove our resource caps
// (unbounded scan → DoS). We strip string literals + comments first so a
// literal containing the word "settings" doesn't false-positive.
func blockedClickHouseClause(q string) string {
	scrubbed := chCommentRe.ReplaceAllString(q, " ")
	scrubbed = chStringLiteralRe.ReplaceAllString(scrubbed, "''")
	if chSettingsRe.MatchString(scrubbed) {
		return "query-level SETTINGS is not allowed in the SQL browser"
	}
	return ""
}

// chStringLiteralRe matches single-quoted string literals (with backslash and
// doubled-quote escapes) so their contents can be blanked before keyword scans.
var chStringLiteralRe = regexp.MustCompile(`'(?:\\.|''|[^'\\])*'`)

// blockedClickHouseBuiltin rejects the SELECT-shaped table functions that reach
// the filesystem or the network. readonly=2 already forbids table functions, so
// this is defence-in-depth — but it must still resist evasion: we strip
// comments, then match function-name-before-paren (tolerating whitespace like
// `file (...)`), not a naive "file(" substring.
func blockedClickHouseBuiltin(q string) string {
	lower := strings.ToLower(chCommentRe.ReplaceAllString(q, " "))
	for _, m := range chFuncCallRe.FindAllStringSubmatch(lower, -1) {
		if reason, bad := blockedFunctions[m[1]]; bad {
			return reason
		}
	}
	// INFILE / OUTFILE are not function calls (they follow a keyword), so match
	// them as bare words.
	if strings.Contains(lower, "infile") {
		return "filesystem access (INFILE)"
	}
	if strings.Contains(lower, "outfile") {
		return "filesystem access (INTO OUTFILE)"
	}
	return ""
}

// runClickHouseQuery executes a read-only query over the ClickHouse HTTP
// interface and returns the columns + string rows, capped at `limit`. It sets
// readonly=2 + max_execution_time server-side so a hostile query can't write
// or run unbounded. Uses JSONCompact so we get typed columns without guessing
// delimiters.
func (h *BackupsHandler) runClickHouseQuery(ctx context.Context, info chConnInfo, query string, limit int) (SQLQueryResponse, int, error) {
	// We enforce the row cap by wrapping the user's query in
	// `SELECT * FROM (…) LIMIT n`. That's precise (URL max_result_rows only
	// caps at block granularity), but it also means the user string is
	// concatenated inside our parens — so a query like `SELECT 1) SETTINGS
	// max_execution_time=0 --` could close the subquery early and append an
	// OUTER settings clause. Under readonly=2 a user may raise settings other
	// than `readonly` itself, so that would defeat our max_execution_time /
	// max_result_rows caps (an unbounded scan → DoS). A read browser never
	// needs a query-level SETTINGS clause, so we reject it outright, which
	// closes the breakout regardless of how the query is shaped.
	if reason := blockedClickHouseClause(query); reason != "" {
		return SQLQueryResponse{}, http.StatusForbidden, fmt.Errorf("query rejected: %s", reason)
	}
	wrapped := fmt.Sprintf("SELECT * FROM (\n%s\n) LIMIT %d", strings.TrimRight(strings.TrimSpace(query), ";"), limit+1)

	q := url.Values{}
	if info.database != "" {
		q.Set("database", info.database)
	}
	q.Set("default_format", "JSONCompact")
	// readonly=2: SELECT-only + may set session settings, but no INSERT/ALTER/
	// CREATE/DROP/etc. max_execution_time bounds runtime. max_result_rows is a
	// second belt on top of our LIMIT wrap.
	q.Set("readonly", "2")
	q.Set("max_execution_time", "5")
	q.Set("max_result_rows", fmt.Sprintf("%d", limit+1))
	q.Set("result_overflow_mode", "break")

	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, info.baseURL+"/?"+q.Encode(), strings.NewReader(wrapped))
	if err != nil {
		return SQLQueryResponse{}, http.StatusBadGateway, err
	}
	req.Header.Set("Content-Type", "text/plain")
	if info.user != "" {
		req.Header.Set("X-ClickHouse-User", info.user)
	}
	if info.password != "" {
		req.Header.Set("X-ClickHouse-Key", info.password)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SQLQueryResponse{}, http.StatusBadGateway, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20)) // 8 MiB cap
	if resp.StatusCode != http.StatusOK {
		// ClickHouse returns a plaintext error body; surface it as a 422 so the
		// client shows the DB message (bad SQL, readonly violation, etc.).
		return SQLQueryResponse{}, http.StatusUnprocessableEntity, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}

	out, err := parseClickHouseJSONCompact(body, limit)
	if err != nil {
		return SQLQueryResponse{}, http.StatusBadGateway, err
	}
	return out, http.StatusOK, nil
}

// parseClickHouseJSONCompact turns a ClickHouse JSONCompact body into the
// shared SQLQueryResponse (string columns + string rows). We requested limit+1
// rows; if we got more than `limit`, mark Truncated and drop the extra.
func parseClickHouseJSONCompact(body []byte, limit int) (SQLQueryResponse, error) {
	var doc struct {
		Meta []struct {
			Name string `json:"name"`
		} `json:"meta"`
		Data [][]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return SQLQueryResponse{}, fmt.Errorf("parse clickhouse response: %w", err)
	}
	out := SQLQueryResponse{Columns: make([]string, len(doc.Meta))}
	for i, m := range doc.Meta {
		out.Columns[i] = m.Name
	}
	rows := doc.Data
	if len(rows) > limit {
		out.Truncated = true
		rows = rows[:limit]
	}
	out.Rows = make([][]string, 0, len(rows))
	for _, r := range rows {
		sr := make([]string, len(r))
		for i, cell := range r {
			sr[i] = stringifyJSONCell(cell)
		}
		out.Rows = append(out.Rows, sr)
	}
	return out, nil
}

// stringifyJSONCell renders one JSONCompact cell as a plain string: JSON
// strings are unquoted; everything else (numbers, bools, nulls, nested
// arrays/objects) is passed through as its compact JSON text. Matches the
// "browsing, not aggregating" contract of stringifyCell on the pg path.
func stringifyJSONCell(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return trimmed
}

// ---------------------------------------------------------------------------
// Structured data browser (read-only) for ClickHouse addons.
//
// ClickHouse maps onto the browser's "schema.table" model as database.table.
// It supports full READ (tables, columns, paginated rows) but NOT the editor's
// row-write model: UPDATE/DELETE in ClickHouse are asynchronous, partition-
// rewriting ALTER … mutations, not transactional single-row edits, and there's
// no RETURNING. So CH tables are surfaced as read-only (Editable=false, same as
// a Postgres table with no primary key), and the write endpoints reject CH with
// a clear message rather than faking a dangerous mutation.
// ---------------------------------------------------------------------------

// chSelect runs a read query (readonly=2) and returns the parsed JSONCompact
// result. Shared by the introspection helpers below and the rows browser.
func (h *BackupsHandler) chSelect(ctx context.Context, info chConnInfo, sqlText string) (SQLQueryResponse, error) {
	q := url.Values{}
	if info.database != "" {
		q.Set("database", info.database)
	}
	q.Set("default_format", "JSONCompact")
	q.Set("readonly", "2")
	q.Set("max_execution_time", "10")

	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, info.baseURL+"/?"+q.Encode(), strings.NewReader(sqlText))
	if err != nil {
		return SQLQueryResponse{}, err
	}
	req.Header.Set("Content-Type", "text/plain")
	if info.user != "" {
		req.Header.Set("X-ClickHouse-User", info.user)
	}
	if info.password != "" {
		req.Header.Set("X-ClickHouse-Key", info.password)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return SQLQueryResponse{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if resp.StatusCode != http.StatusOK {
		return SQLQueryResponse{}, fmt.Errorf("%s", strings.TrimSpace(string(body)))
	}
	return parseClickHouseJSONCompact(body, 1<<30)
}

// chIdentifier quotes a ClickHouse identifier (database/table/column) with
// backticks. ClickHouse processes backslash escapes inside backtick-quoted
// identifiers (like string literals), so a trailing `\` would escape the
// closing backtick and break out — we must escape backslash BEFORE the
// backtick, mirroring chStringLiteral. pq.QuoteIdentifier (double-quotes) is
// wrong for ClickHouse. Callers still pass only allowlisted (exists-checked)
// identifiers; this is belt-and-suspenders.
func chIdentifier(id string) string {
	id = strings.ReplaceAll(id, "\\", "\\\\")
	id = strings.ReplaceAll(id, "`", "\\`")
	return "`" + id + "`"
}

// chStringLiteral single-quote-escapes a value for a WHERE = '...' literal.
func chStringLiteral(v string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(v, "\\", "\\\\"), "'", "\\'") + "'"
}

// clickhouseTableExists confirms database.table is a real table.
func (h *BackupsHandler) clickhouseTableExists(ctx context.Context, info chConnInfo, database, table string) (bool, error) {
	sqlText := fmt.Sprintf(
		"SELECT count() FROM system.tables WHERE database = %s AND name = %s",
		chStringLiteral(database), chStringLiteral(table),
	)
	out, err := h.chSelect(ctx, info, sqlText)
	if err != nil {
		return false, err
	}
	if len(out.Rows) == 0 || len(out.Rows[0]) == 0 {
		return false, nil
	}
	return out.Rows[0][0] != "0" && out.Rows[0][0] != "", nil
}

// clickhouseColumns introspects a table via system.columns and returns the
// shared columnsResponse. Nullability + enum values are derived from the CH
// type string. Editable is always false — see the file header.
func (h *BackupsHandler) clickhouseColumns(ctx context.Context, info chConnInfo, database, table string) (columnsResponse, error) {
	sqlText := fmt.Sprintf(`SELECT name, type, default_expression, is_in_primary_key, position
		FROM system.columns
		WHERE database = %s AND table = %s
		ORDER BY position`,
		chStringLiteral(database), chStringLiteral(table))
	res, err := h.chSelect(ctx, info, sqlText)
	if err != nil {
		return columnsResponse{}, err
	}
	var out columnsResponse
	for _, r := range res.Rows {
		if len(r) < 5 {
			continue
		}
		name, chType, def, isPK, pos := r[0], r[1], r[2], r[3], r[4]
		ci := columnInfo{
			Name:     name,
			DataType: chType,
			UDTName:  chType,
			Nullable: strings.Contains(chType, "Nullable("),
			Default:  def,
		}
		if n, perr := strconv.Atoi(pos); perr == nil {
			ci.Ordinal = n
		}
		if vals, ok := parseClickHouseEnum(chType); ok {
			ci.IsEnum = true
			ci.EnumValues = vals
		}
		out.Columns = append(out.Columns, ci)
		if isPK == "1" {
			out.PrimaryKey = append(out.PrimaryKey, name)
		}
	}
	// ClickHouse tables are not row-editable via this browser (async ALTER
	// mutations, no single-row transactional writes). Surface as read-only.
	out.Editable = false
	return out, nil
}

// parseClickHouseEnum extracts the labels of an Enum8/Enum16 type, e.g.
// Enum8('a' = 1, 'b' = 2) → ["a","b"]. Handles the enum wrapped in
// Nullable(...) / LowCardinality(...) too, since we scan for "Enum".
func parseClickHouseEnum(chType string) ([]string, bool) {
	i := strings.Index(chType, "Enum")
	if i < 0 {
		return nil, false
	}
	open := strings.Index(chType[i:], "(")
	if open < 0 {
		return nil, false
	}
	rest := chType[i+open+1:]
	end := strings.LastIndex(rest, ")")
	if end < 0 {
		return nil, false
	}
	var labels []string
	for _, part := range strings.Split(rest[:end], ",") {
		part = strings.TrimSpace(part)
		// each part is 'label' = N — take the quoted label
		if q1 := strings.Index(part, "'"); q1 >= 0 {
			if q2 := strings.Index(part[q1+1:], "'"); q2 >= 0 {
				labels = append(labels, part[q1+1:q1+1+q2])
			}
		}
	}
	return labels, len(labels) > 0
}

// clickhouseRows renders a paginated single-table read. dir/orderBy are already
// validated (orderBy must be a real column). Returns the shared rowsResponse.
func (h *BackupsHandler) clickhouseRows(ctx context.Context, info chConnInfo, database, table, orderBy, dir string, limit, offset int) (rowsResponse, error) {
	q := chIdentifier(database) + "." + chIdentifier(table)
	var b strings.Builder
	fmt.Fprintf(&b, "SELECT * FROM %s", q)
	if orderBy != "" {
		fmt.Fprintf(&b, " ORDER BY %s %s", chIdentifier(orderBy), validClickHouseDir(dir))
	}
	// limit+1 to detect truncation, matching the pg path.
	fmt.Fprintf(&b, " LIMIT %d OFFSET %d", limit+1, offset)

	res, err := h.chSelect(ctx, info, b.String())
	if err != nil {
		return rowsResponse{}, err
	}
	out := rowsResponse{Columns: res.Columns, Rows: [][]string{}, Nulls: [][]bool{}}
	for i, r := range res.Rows {
		if i >= limit {
			out.Truncated = true
			break
		}
		nulls := make([]bool, len(r))
		for j := range r {
			// JSONCompact renders SQL NULL as JSON null → we mapped it to "".
			// We can't perfectly distinguish "" from NULL post-stringify, so
			// mark empty cells on Nullable columns as null best-effort: leave
			// false here (the pg path has real null info; CH over HTTP loses
			// it). Empty string is the safe display.
			nulls[j] = false
		}
		out.Rows = append(out.Rows, r)
		out.Nulls = append(out.Nulls, nulls)
	}

	// Total row count for pagination.
	cnt, cerr := h.chSelect(ctx, info, "SELECT count() FROM "+q)
	if cerr == nil && len(cnt.Rows) > 0 && len(cnt.Rows[0]) > 0 {
		if n, perr := strconv.Atoi(cnt.Rows[0][0]); perr == nil {
			out.Total = n
		}
	}
	return out, nil
}

func validClickHouseDir(dir string) string {
	if strings.EqualFold(strings.TrimSpace(dir), "desc") {
		return "DESC"
	}
	return "ASC"
}
