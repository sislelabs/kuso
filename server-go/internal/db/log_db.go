// log_db.go — log search + alert support against the unified Postgres
// DB. The pre-v0.9 split into a separate SQLite file was a workaround
// for SQLite's single-writer model. Postgres handles concurrent writes
// natively, so the LogDB type is now a thin alias around *DB; the
// pre-existing call sites on LogDB keep compiling without changes.
//
// Search: dropped the SQLite FTS5 path. Postgres tsvector would be
// the equivalent here, but for kuso's working volume the LIKE-based
// scan over the (project, service, ts) index is fast enough and
// keeps the schema simple. If full-text becomes a need, swap the
// LIKE for `to_tsvector('english', line) @@ plainto_tsquery(?)` plus
// a generated column + GIN index.

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// LogDB is an alias type around *DB to keep the call-site shape from
// pre-v0.9 (logship.Shipper, alerts.Engine, log search handler) the
// same. Methods defined on *LogDB delegate to the embedded *DB.
type LogDB struct {
	*DB
}

// AsLogDB returns a *LogDB view of the same underlying Postgres
// connection. Cheap (one struct allocation) — call once at startup
// in main.go and pass the result around like the old separate handle.
func (d *DB) AsLogDB() *LogDB {
	return &LogDB{DB: d}
}

// InsertLogLines batches a slice of lines in one transaction. Caller
// is responsible for buffering up sane batch sizes (logship flushes
// every 1s or 500 lines).
func (d *LogDB) InsertLogLines(ctx context.Context, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	// We can't use d.QueryContext's `?` rewriter through tx.Prepare,
	// so we write Postgres-native placeholders here.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO "LogLine" ("ts","pod","project","service","env","line")
		VALUES ($1,$2,$3,$4,$5,$6)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, l := range lines {
		// Strip null bytes — Postgres rejects them outright in TEXT
		// columns (invalid UTF-8 sequence).
		line := strings.ReplaceAll(l.Line, "\x00", "")
		if _, err := stmt.ExecContext(ctx, l.Ts.UTC(), l.Pod, l.Project, l.Service, l.Env, line); err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}
	return tx.Commit()
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
	sqlStr.WriteString(`SELECT id, ts, pod, project, service, env, line FROM "LogLine" WHERE 1=1`)
	if q != "" {
		// Case-insensitive LIKE. Postgres ILIKE is the cheap path; if
		// the working set ever needs ranked match, swap for tsvector.
		// Escape user-supplied %, _, and \ — without this a search for
		// "100%" matches every line containing "100" (the % becomes
		// the wildcard) and "user_id" wildcards every char between
		// "user" and "id".
		sqlStr.WriteString(` AND line ILIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if req.Project != "" {
		sqlStr.WriteString(` AND project = ?`)
		args = append(args, req.Project)
	}
	if req.Service != "" {
		sqlStr.WriteString(` AND service = ?`)
		args = append(args, req.Service)
	}
	if req.Env != "" {
		sqlStr.WriteString(` AND env = ?`)
		args = append(args, req.Env)
	}
	if !req.Since.IsZero() {
		sqlStr.WriteString(` AND ts >= ?`)
		args = append(args, req.Since.UTC())
	}
	if !req.Until.IsZero() {
		sqlStr.WriteString(` AND ts < ?`)
		args = append(args, req.Until.UTC())
	}
	sqlStr.WriteString(` ORDER BY id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := d.DB.QueryContext(ctx, sqlStr.String(), args...)
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
func (d *LogDB) CountLogMatches(ctx context.Context, project, service, query string, since time.Time) (int, error) {
	q := strings.TrimSpace(query)
	args := []any{}
	var sqlStr strings.Builder
	sqlStr.WriteString(`SELECT COUNT(*) FROM "LogLine" WHERE 1=1`)
	if q != "" {
		sqlStr.WriteString(` AND line ILIKE ? ESCAPE '\'`)
		args = append(args, "%"+escapeLike(q)+"%")
	}
	if project != "" {
		sqlStr.WriteString(` AND project = ?`)
		args = append(args, project)
	}
	if service != "" {
		sqlStr.WriteString(` AND service = ?`)
		args = append(args, service)
	}
	if !since.IsZero() {
		sqlStr.WriteString(` AND ts >= ?`)
		args = append(args, since.UTC())
	}
	row := d.DB.QueryRowContext(ctx, sqlStr.String(), args...)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count log matches: %w", err)
	}
	return n, nil
}

// PruneLogsOlderThan deletes rows older than `before`. Returns the
// number of rows removed.
func (d *LogDB) PruneLogsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "LogLine" WHERE ts < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune logs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// escapeLike escapes the SQL LIKE / ILIKE wildcards `%`, `_`, and the
// escape char `\` itself. Pair with `ESCAPE '\'` on the query — without
// that clause Postgres falls back to the empty escape char and ignores
// any backslashes we put in here.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
