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
	releaseName := addons.CRName(project, addon)
	connSecret := addons.ConnSecretName(releaseName)
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, connSecret, metav1.GetOptions{})
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

// blockedClickHouseBuiltin rejects the SELECT-shaped table functions that reach
// the filesystem or the network. readonly mode blocks writes/DDL; this is the
// ONLY control for these read-side vectors, so it must resist evasion: we strip
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
	// Wrap the user's query so the row cap is enforced server-side even if they
	// didn't add a LIMIT. We ask for limit+1 rows to detect truncation.
	// Subquery form works for SELECTs; readonly mode rejects anything else.
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
