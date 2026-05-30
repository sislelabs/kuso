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
	"fmt"
	"strings"
	"time"
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

func (d *LogDB) insertLogLinesChunk(ctx context.Context, chunk []LogLine) error {
	if len(chunk) == 0 {
		return nil
	}
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
		// Strip null bytes — Postgres rejects them outright in TEXT
		// columns (invalid UTF-8 sequence).
		line := strings.ReplaceAll(l.Line, "\x00", "")
		args = append(args, l.Ts.UTC(), l.Pod, l.Project, l.Service, l.Env, line)
	}
	if _, err := d.DB.DB.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert log lines: %w", err)
	}
	return nil
}

// SearchLogs runs the search using LIKE filters. Returns newest-first.
// FTS5 is gone in v0.9 — the wire shape and call sites stay the same,
// the query path is just simpler.
func (d *LogDB) SearchLogs(ctx context.Context, req SearchLogsRequest) ([]LogLine, error) {
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := strings.TrimSpace(req.Query)
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
	n++
	fmt.Fprintf(&sqlStr, ` ORDER BY id DESC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := d.QueryContext(ctx, sqlStr.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("search logs: %w", err)
	}
	defer rows.Close()
	out := []LogLine{}
	for rows.Next() {
		var ll LogLine
		var ts sql.NullTime
		if err := rows.Scan(&ll.ID, &ts, &ll.Pod, &ll.Project, &ll.Service, &ll.Env, &ll.Line); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if ts.Valid {
			ll.Ts = ts.Time
		}
		out = append(out, ll)
	}
	return out, rows.Err()
}

// CountLogMatches is the alert-rule helper: how many matches in a
// given window. Empty query counts every line.
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
	sqlStr.WriteString(`SELECT COUNT(*) FROM "LogLine" WHERE 1=1`)
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
	row := d.QueryRowContext(ctx, sqlStr.String(), args...)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count log matches: %w", err)
	}
	return n, nil
}

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
		// Postgres lacks DELETE ... LIMIT, so we use the
		// `WHERE ctid IN (SELECT ctid ... LIMIT N)` keyset trick.
		// ctid is the physical row pointer — fastest possible probe.
		res, err := d.ExecContext(ctx, `
DELETE FROM "LogLine"
WHERE ctid IN (
  SELECT ctid FROM "LogLine"
  WHERE ts < $1
  LIMIT $2
)`, before.UTC(), chunk)
		if err != nil {
			return total, fmt.Errorf("prune logs: %w", err)
		}
		n, _ := res.RowsAffected()
		total += n
		if n < chunk {
			return total, nil
		}
	}
}

// escapeLike escapes the SQL LIKE / ILIKE wildcards `%`, `_`, and the
// escape char `\` itself. Pair with `ESCAPE '\'` on the query — without
// that clause Postgres falls back to the empty escape char and ignores
// any backslashes we put in here.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
