// log_db.go — log search + alert support against the Postgres DB.
// LogDB is a thin alias type around *DB, kept distinct from *DB so
// the log-search/alerts wiring (logship.Shipper, alerts.Engine) has
// its own typed handle.
//
// Search uses LIKE over the (project, service, ts) index. If
// full-text ever becomes a need, swap the LIKE for
// `to_tsvector('english', line) @@ plainto_tsquery($1)` plus a
// generated column + GIN index.

package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lib/pq"
)

// LogDB is an alias type around *DB used by the log-search and alerts
// wiring (logship.Shipper, alerts.Engine, the /api/logs/search handler)
// so those modules carry a distinct type from generic *DB.
type LogDB struct {
	*DB
}

// AsLogDB returns a *LogDB view of the same underlying Postgres
// connection. Cheap (one struct allocation) — call once at startup
// in main.go and pass the result around like the old separate handle.
func (d *DB) AsLogDB() *LogDB {
	return &LogDB{DB: d}
}

// InsertLogLines batches a slice of lines into a single multi-VALUES
// INSERT — one round-trip per batch instead of N (the previous
// prepare-then-Exec-per-row pattern was capped at ~1 line per
// network RTT). Postgres's parameter limit is ~65k per statement;
// 6 columns × 5k = 30k stays well below that, and we cap any larger
// batch into smaller chunks.
//
// Caller still buffers — logship flushes every 1s or 500 lines —
// but operators with a chatty workload that pushes that envelope
// no longer hit the per-statement RTT ceiling.
func (d *LogDB) InsertLogLines(ctx context.Context, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	const maxPerInsert = 5000
	for start := 0; start < len(lines); start += maxPerInsert {
		end := start + maxPerInsert
		if end > len(lines) {
			end = len(lines)
		}
		if err := d.insertLogLinesChunk(ctx, lines[start:end]); err != nil {
			return err
		}
	}
	return nil
}

// MaxLogLineChars is LogLine_line_len_chk's limit. Postgres length()
// counts characters, not bytes.
const MaxLogLineChars = 16384

const logLineTruncSuffix = "…[truncated]"

// SanitizeLogLine makes s storable in LogLine.line: invalid UTF-8 is
// replaced, NUL bytes (rejected by Postgres TEXT) are removed, and the
// result, including the truncation suffix, fits the length CHECK.
// Truncation is by rune so a multi-byte character is never split.
func SanitizeLogLine(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if len(s) <= MaxLogLineChars || utf8.RuneCountInString(s) <= MaxLogLineChars {
		return s
	}
	keep := MaxLogLineChars - utf8.RuneCountInString(logLineTruncSuffix)
	n := 0
	for i := range s {
		if n == keep {
			return s[:i] + logLineTruncSuffix
		}
		n++
	}
	return s
}

func (d *LogDB) insertLogLinesChunk(ctx context.Context, chunk []LogLine) error {
	if len(chunk) == 0 {
		return nil
	}
	err := d.insertLogLinesStmt(ctx, chunk)
	if err == nil || !isRowDataError(err) {
		return err
	}
	// One bad row fails the whole multi-VALUES statement, and the
	// shipper re-queues the batch — so a single unstorable line used to
	// block every line queued behind it until the buffer cap dropped
	// them all. Bisect to isolate and drop just the offending rows.
	if len(chunk) == 1 {
		d.Stats.WriteErrors.Add(1)
		return nil
	}
	mid := len(chunk) / 2
	if err := d.insertLogLinesChunk(ctx, chunk[:mid]); err != nil {
		return err
	}
	return d.insertLogLinesChunk(ctx, chunk[mid:])
}

// isRowDataError reports whether err is caused by a row's content
// (data exception class 22 or integrity violation class 23) rather
// than the connection or the database being unavailable.
func isRowDataError(err error) bool {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return false
	}
	class := string(pqErr.Code.Class())
	return class == "22" || class == "23"
}

func (d *LogDB) insertLogLinesStmt(ctx context.Context, chunk []LogLine) error {
	const cols = 6
	args := make([]any, 0, len(chunk)*cols)
	var sb strings.Builder
	sb.WriteString(`INSERT INTO "LogLine" ("ts","pod","project","service","env","line") VALUES `)
	for i, l := range chunk {
		if i > 0 {
			sb.WriteByte(',')
		}
		base := i*cols + 1
		fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d)",
			base, base+1, base+2, base+3, base+4, base+5)
		args = append(args, l.Ts.UTC(), l.Pod, l.Project, l.Service, l.Env, SanitizeLogLine(l.Line))
	}
	if _, err := d.DB.DB.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert log lines: %w", err)
	}
	return nil
}

// LatestPodLogTs returns the newest stored ts for one pod (zero when it
// has none). since is the pod's creation time: it bounds the
// (project, service, ts DESC) index scan to the pod's own lifetime, so a
// never-shipped pod doesn't walk the service's whole history.
func (d *LogDB) LatestPodLogTs(ctx context.Context, project, service, pod string, since time.Time) (time.Time, error) {
	var ts sql.NullTime
	err := d.DB.DB.QueryRowContext(ctx,
		`SELECT max("ts") FROM "LogLine" WHERE "project" = $1 AND "service" = $2 AND "pod" = $3 AND "ts" >= $4`,
		project, service, pod, since.UTC()).Scan(&ts)
	if err != nil {
		return time.Time{}, fmt.Errorf("latest pod log ts: %w", err)
	}
	if !ts.Valid {
		return time.Time{}, nil
	}
	return ts.Time, nil
}

// PodLinesSince returns the text of one pod's lines stored at or after
// since (ingest time), at most limit of them. logship uses it to drop the
// lines it replays when resuming a pod after a leader change.
func (d *LogDB) PodLinesSince(ctx context.Context, project, service, pod string, since time.Time, limit int) ([]string, error) {
	rows, err := d.DB.DB.QueryContext(ctx,
		`SELECT "line" FROM "LogLine"
		  WHERE "project" = $1 AND "service" = $2 AND "pod" = $3 AND "ts" >= $4
		  ORDER BY "ts" DESC, "id" DESC LIMIT $5`,
		project, service, pod, since.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("pod lines since: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("pod lines since scan: %w", err)
		}
		out = append(out, line)
	}
	return out, rows.Err()
}

// PodLogTail returns the last n archived lines of the pods whose names
// start with podPrefix, oldest-first. since bounds the index scan to the
// pods' lifetime. cronwatch uses it for a failed Job whose pod the Job
// controller has already deleted.
func (d *LogDB) PodLogTail(ctx context.Context, project, service, podPrefix string, since time.Time, n int) ([]string, error) {
	rows, err := d.DB.DB.QueryContext(ctx,
		`SELECT "line" FROM "LogLine"
		  WHERE "project" = $1 AND "service" = $2 AND "ts" >= $3 AND "pod" LIKE $4 ESCAPE '\'
		  ORDER BY "ts" DESC, "id" DESC LIMIT $5`,
		project, service, since.UTC(), escapeLike(podPrefix)+"%", n)
	if err != nil {
		return nil, fmt.Errorf("pod log tail: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("pod log tail scan: %w", err)
		}
		out = append(out, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pod log tail: %w", err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MaxLogSearchLimit caps a single page of log search results.
const MaxLogSearchLimit = 500

// MinLogQueryRunes is the shortest substring query SearchLogs accepts.
// The trigram index can't serve a 1-2 character ILIKE, so such a query
// was a parallel seq scan of the whole LogLine.
const MinLogQueryRunes = 3

var (
	// ErrLogQueryTooShort: a non-empty query under MinLogQueryRunes.
	ErrLogQueryTooShort = errors.New("log search query too short")
	// ErrLogSearchTooBroad: the query hit logQueryTimeout (or its caller's
	// deadline) before finding enough rows.
	ErrLogSearchTooBroad = errors.New("log search too broad")
)

// logQueryTimeout caps one log search / alert count server-side, so a
// scan that the planner can't bound gives up instead of holding a pooled
// connection and parallel workers for the role's full 60s.
var logQueryTimeout = "8s"

// logCountCap bounds CountLogMatches: alert rules compare against a
// threshold, so counting past it only costs scan time.
var logCountCap = 10_000

// withLogQueryTimeout runs fn in a read transaction carrying
// logQueryTimeout. SET LOCAL is transaction-scoped, so it is safe behind
// the transaction-mode pooler.
func (d *LogDB) withLogQueryTimeout(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.DB.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '`+logQueryTimeout+`'`); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		if isQueryCanceled(err) {
			return fmt.Errorf("%w: %w", ErrLogSearchTooBroad, err)
		}
		return err
	}
	return tx.Commit()
}

// isQueryCanceled reports a statement_timeout or cancel (SQLSTATE 57014).
func isQueryCanceled(err error) bool {
	var pqErr *pq.Error
	return errors.As(err, &pqErr) && pqErr.Code == "57014"
}

// ClampLogLimit normalises a caller-supplied page size. Exported so the
// HTTP layer can distinguish a full page from a short one (and so emit
// a paging cursor) without duplicating the clamp and drifting from it.
func ClampLogLimit(limit int) int {
	if limit <= 0 || limit > MaxLogSearchLimit {
		return MaxLogSearchLimit
	}
	return limit
}

// SearchLogs runs the search using LIKE filters. Returns newest-first.
// FTS5 is gone in v0.9 — the wire shape and call sites stay the same,
// the query path is just simpler.
func (d *LogDB) SearchLogs(ctx context.Context, req SearchLogsRequest) ([]LogLine, error) {
	limit := ClampLogLimit(req.Limit)
	q := strings.TrimSpace(req.Query)
	if q != "" && utf8.RuneCountInString(q) < MinLogQueryRunes {
		return nil, fmt.Errorf("%w: need at least %d characters", ErrLogQueryTooShort, MinLogQueryRunes)
	}
	var sqlStr strings.Builder
	args := []any{}
	n := 0
	sqlStr.WriteString(`SELECT id, ts, pod, project, service, env, line FROM "LogLine" WHERE 1=1`)
	if q != "" {
		// Case-insensitive LIKE. Postgres ILIKE is the cheap path; if
		// the working set ever needs ranked match, swap for tsvector.
		// Escape user-supplied %, _, and \ — without this a search for
		// "100%" matches every line containing "100" (the % becomes
		// the wildcard) and "user_id" wildcards every char between
		// "user" and "id".
		n++
		fmt.Fprintf(&sqlStr, ` AND line ILIKE $%d ESCAPE '\'`, n)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if req.Project != "" {
		n++
		fmt.Fprintf(&sqlStr, ` AND project = $%d`, n)
		args = append(args, req.Project)
	}
	if req.Service != "" {
		n++
		fmt.Fprintf(&sqlStr, ` AND service = $%d`, n)
		args = append(args, req.Service)
	}
	if req.Env != "" {
		n++
		fmt.Fprintf(&sqlStr, ` AND env = $%d`, n)
		args = append(args, req.Env)
	}
	if !req.Since.IsZero() {
		n++
		fmt.Fprintf(&sqlStr, ` AND ts >= $%d`, n)
		args = append(args, req.Since.UTC())
	}
	if !req.Until.IsZero() {
		n++
		fmt.Fprintf(&sqlStr, ` AND ts < $%d`, n)
		args = append(args, req.Until.UTC())
	}
	forward := req.BeforeID <= 0 && req.AfterID > 0
	if req.BeforeID > 0 {
		n++
		fmt.Fprintf(&sqlStr, ` AND id < $%d`, n)
		args = append(args, req.BeforeID)
	} else if forward {
		n++
		fmt.Fprintf(&sqlStr, ` AND id > $%d`, n)
		args = append(args, req.AfterID)
	}
	n++
	if forward {
		fmt.Fprintf(&sqlStr, ` ORDER BY id ASC LIMIT $%d`, n)
	} else {
		fmt.Fprintf(&sqlStr, ` ORDER BY id DESC LIMIT $%d`, n)
	}
	args = append(args, limit)

	out := []LogLine{}
	err := d.withLogQueryTimeout(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, sqlStr.String(), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var ll LogLine
			var ts sql.NullTime
			if err := rows.Scan(&ll.ID, &ts, &ll.Pod, &ll.Project, &ll.Service, &ll.Env, &ll.Line); err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			if ts.Valid {
				ll.Ts = ts.Time
			}
			out = append(out, ll)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search logs: %w", err)
	}
	// Forward pages are fetched oldest-first (so the LIMIT keeps the rows
	// nearest the anchor) but returned newest-first like every other page.
	if forward {
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
	}
	return out, nil
}

// CountLogMatches is the alert-rule helper: how many matches in a
// given window, capped at logCountCap. Empty query counts every line.
//
// Window safety: even if the alert rule says "last 7 days" we cap
// the actual scan at 24h. Anything wider would be a sequential scan
// the trigram index can't help much with, and rules with that wide
// a window are almost always a mistake — they make every tick a
// big query and the alerted-condition only ever resolves on rule
// edit. Operators who really need long windows should pre-aggregate.
func (d *LogDB) CountLogMatches(ctx context.Context, project, service, query string, since time.Time) (int, error) {
	q := strings.TrimSpace(query)
	const maxWindow = 24 * time.Hour
	cutoff := time.Now().Add(-maxWindow)
	if since.Before(cutoff) {
		since = cutoff
	}
	args := []any{}
	p := 0
	var sqlStr strings.Builder
	sqlStr.WriteString(`SELECT COUNT(*) FROM (SELECT 1 FROM "LogLine" WHERE 1=1`)
	if q != "" {
		p++
		fmt.Fprintf(&sqlStr, ` AND line ILIKE $%d ESCAPE '\'`, p)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if project != "" {
		p++
		fmt.Fprintf(&sqlStr, ` AND project = $%d`, p)
		args = append(args, project)
	}
	if service != "" {
		p++
		fmt.Fprintf(&sqlStr, ` AND service = $%d`, p)
		args = append(args, service)
	}
	if !since.IsZero() {
		p++
		fmt.Fprintf(&sqlStr, ` AND ts >= $%d`, p)
		args = append(args, since.UTC())
	}
	p++
	fmt.Fprintf(&sqlStr, ` LIMIT $%d) m`, p)
	args = append(args, logCountCap)
	var n int
	err := d.withLogQueryTimeout(ctx, func(tx *sql.Tx) error {
		return tx.QueryRowContext(ctx, sqlStr.String(), args...).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("count log matches: %w", err)
	}
	return n, nil
}

// LogCountCapped reports whether a CountLogMatches result hit its cap.
func LogCountCapped(n int) bool { return n >= logCountCap }

// PruneLogsOlderThan deletes rows older than `before` in chunks of
// at most 50k per statement. Returns the total rows removed.
//
// One unbounded `DELETE ... WHERE ts < ?` on a 200GB LogLine takes a
// table-locking write that blocks the logship inserter for seconds —
// at 50 lines/pod/min × hundreds of pods, the buffer fills, the
// flusher starts dropping, and ingest gaps appear in the UI right
// when an operator is most likely to be looking. Bounded chunks keep
// each transaction short so the LogLine_project_service_ts_idx
// b-tree stays available to concurrent readers.
//
// Iterates until a chunk deletes fewer than the chunk size, signal-
// ling we've drained everything older than `before`. Caller can call
// us with a context deadline to bound total work; we honour
// ctx.Done() between chunks.
func (d *LogDB) PruneLogsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	const chunk = 50000
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		// Postgres lacks DELETE ... LIMIT, so we select a bounded set of
		// physical row pointers and delete those. ctid alone is only
		// unique within ONE relation: on a partitioned LogLine the same
		// (block,offset) exists in every day-partition, so a bare
		// `ctid IN (...)` also deleted today's rows at the tids of old
		// ones. (tableoid, ctid) names a row uniquely across partitions.
		n, err := d.pruneLogChunk(ctx, before, chunk)
		if err != nil {
			return total, fmt.Errorf("prune logs: %w", err)
		}
		total += n
		if n < chunk {
			return total, nil
		}
	}
}

// pruneLogChunk deletes one chunk in its own transaction with a longer
// statement timeout than the role's request-path default: a 50k-row chunk
// on a cold, large LogLine can legitimately take more than a minute.
func (d *LogDB) pruneLogChunk(ctx context.Context, before time.Time, chunk int) (int64, error) {
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '10min'`); err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
DELETE FROM "LogLine"
WHERE (tableoid, ctid) IN (
  SELECT tableoid, ctid FROM "LogLine"
  WHERE ts < $1
  LIMIT $2
)`, before.UTC(), chunk)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, tx.Commit()
}

// escapeLike escapes the SQL LIKE / ILIKE wildcards `%`, `_`, and the
// escape char `\` itself. Pair with `ESCAPE '\'` on the query — without
// that clause Postgres falls back to the empty escape char and ignores
// any backslashes we put in here.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
