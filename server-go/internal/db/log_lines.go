// Searchable log storage. The logship goroutine streams pod logs
// into LogLine; FTS5 handles full-text search via the LogLineFts
// virtual table.

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type LogLine struct {
	ID      int64     `json:"id"`
	Ts      time.Time `json:"ts"`
	Pod     string    `json:"pod"`
	Project string    `json:"project,omitempty"`
	Service string    `json:"service,omitempty"`
	Env     string    `json:"env,omitempty"`
	Line    string    `json:"line"`
}

// InsertLogLines batches a slice of lines in one transaction. Caller
// is responsible for buffering up sane batch sizes (e.g. flush every
// 1s or 500 lines).
func (d *DB) InsertLogLines(ctx context.Context, lines []LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	tx, err := d.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO "LogLine" ("ts","pod","project","service","env","line")
		VALUES (?,?,?,?,?,?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer stmt.Close()
	for _, l := range lines {
		// Strip null bytes — SQLite handles them but they break FTS5
		// tokenisation in some unicode locales.
		line := strings.ReplaceAll(l.Line, "\x00", "")
		if _, err := stmt.ExecContext(ctx, l.Ts.UTC(), l.Pod, l.Project, l.Service, l.Env, line); err != nil {
			return fmt.Errorf("insert: %w", err)
		}
	}
	return tx.Commit()
}

// SearchLogsRequest is the wire shape — every field optional except
// project + service (which gate access).
type SearchLogsRequest struct {
	Project string
	Service string
	Env     string
	Query   string    // FTS5 MATCH; empty means "no text filter"
	Since   time.Time // inclusive; zero = no lower bound
	Until   time.Time // exclusive; zero = no upper bound
	Limit   int
}

// SearchLogs runs the search. Returns newest-first (humans want the
// most recent matches by default; the UI flips order when paging).
//
// Two query shapes:
//  - empty req.Query → straight LogLine scan filtered by metadata.
//    Uses the (project,service,ts) index.
//  - non-empty req.Query → FTS5 MATCH joined back to LogLine for
//    metadata filters. The MATCH grammar is FTS5 standard: phrase
//    with quotes, AND/OR/NOT, prefix (foo*).
func (d *DB) SearchLogs(ctx context.Context, req SearchLogsRequest) ([]LogLine, error) {
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	q := strings.TrimSpace(req.Query)
	hasFTS := q != ""
	col := func(name string) string {
		if hasFTS {
			return "l." + name
		}
		return name
	}
	var sqlStr strings.Builder
	args := []any{}
	if hasFTS {
		sqlStr.WriteString(`SELECT l.id, l.ts, l.pod, l.project, l.service, l.env, l.line
			FROM LogLineFts f JOIN LogLine l ON l.id = f.rowid
			WHERE LogLineFts MATCH ?`)
		args = append(args, q)
	} else {
		sqlStr.WriteString(`SELECT id, ts, pod, project, service, env, line FROM LogLine WHERE 1=1`)
	}
	if req.Project != "" {
		sqlStr.WriteString(" AND " + col("project") + " = ?")
		args = append(args, req.Project)
	}
	if req.Service != "" {
		sqlStr.WriteString(" AND " + col("service") + " = ?")
		args = append(args, req.Service)
	}
	if req.Env != "" {
		sqlStr.WriteString(" AND " + col("env") + " = ?")
		args = append(args, req.Env)
	}
	if !req.Since.IsZero() {
		sqlStr.WriteString(" AND " + col("ts") + " >= ?")
		args = append(args, req.Since.UTC())
	}
	if !req.Until.IsZero() {
		sqlStr.WriteString(" AND " + col("ts") + " < ?")
		args = append(args, req.Until.UTC())
	}
	sqlStr.WriteString(" ORDER BY " + col("id") + " DESC LIMIT ?")
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
// given window for a given query? Returns int. Empty query counts
// every line (pod-volume alerting).
func (d *DB) CountLogMatches(ctx context.Context, project, service, query string, since time.Time) (int, error) {
	q := strings.TrimSpace(query)
	args := []any{}
	var sqlStr string
	if q != "" {
		sqlStr = `SELECT COUNT(*) FROM LogLineFts f JOIN LogLine l ON l.id = f.rowid
		          WHERE LogLineFts MATCH ?`
		args = append(args, q)
	} else {
		sqlStr = `SELECT COUNT(*) FROM LogLine WHERE 1=1`
	}
	col := func(name string) string {
		if q != "" {
			return "l." + name
		}
		return name
	}
	if project != "" {
		sqlStr += " AND " + col("project") + " = ?"
		args = append(args, project)
	}
	if service != "" {
		sqlStr += " AND " + col("service") + " = ?"
		args = append(args, service)
	}
	if !since.IsZero() {
		sqlStr += " AND " + col("ts") + " >= ?"
		args = append(args, since.UTC())
	}
	row := d.DB.QueryRowContext(ctx, sqlStr, args...)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count log matches: %w", err)
	}
	return n, nil
}

// PruneLogsOlderThan deletes rows older than `before`. The FTS
// trigger handles index cleanup. Called on a slow ticker.
func (d *DB) PruneLogsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "LogLine" WHERE "ts" < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune logs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

