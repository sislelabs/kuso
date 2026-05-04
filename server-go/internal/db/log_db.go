// LogDB is a separate SQLite database file dedicated to log line
// storage and full-text search. Splitting it out from the main kuso.db
// is the highest-ROI scalability fix: the logship goroutine is the
// single heaviest writer (500-line FTS5 batches every 1s when a chatty
// pod is running), and FTS5 triggers double the write count. With the
// main DB capped at one writer (`SetMaxOpenConns(1)`), every log
// flush blocks audit log inserts, login lookups, notification persists,
// and node-metric samples. By moving LogLine + LogLineFts to their
// own connection pool, the latency-sensitive control plane stops
// contending with the noisy log path.
//
// Wire shape: same modernc.org/sqlite driver, same WAL pragma, same
// single-writer model (FTS5 doesn't benefit from multi-writer either).
// Schema is a strict subset of what was in db.go — only the tables
// the alert engine and log-search handler read.
//
// Migration: existing kuso instances may still have LogLine rows in
// kuso.db from before the split. Those rows age out under 14d
// retention and are no longer written to. Search / alert reads only
// hit the new file; the orphaned rows in kuso.db are harmless and
// pruned by the legacy retention path that still exists in the main
// schema (they just stop receiving inserts).

package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// LogDB wraps the dedicated log-storage SQLite handle. It exposes
// the same API surface that lived on *DB before the split: callers
// (logship.Shipper, alerts.Engine, log search handler) flip from
// `*db.DB` to `*db.LogDB` for log-only operations.
type LogDB struct {
	*sql.DB
}

// OpenLog opens or creates the log-storage SQLite file at path and
// applies the schema. Idempotent. The file is independent from the
// main kuso.db; deleting it loses search history but doesn't affect
// any other state.
func OpenLog(path string) (*LogDB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("logdb: open %s: %w", path, err)
	}
	// Single writer matches the main DB's contention model. SQLite +
	// WAL allows concurrent readers but serialises writes; we keep
	// the connection pool at 1 so the logship batch INSERT and the
	// alert engine's COUNT/SELECT queries don't contend on the
	// process side either.
	sqldb.SetMaxOpenConns(1)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)

	if err := sqldb.Ping(); err != nil {
		return nil, fmt.Errorf("logdb: ping %s: %w", path, err)
	}
	d := &LogDB{DB: sqldb}
	if err := d.applySchema(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// applySchema is the LogLine + LogLineFts subset of the main migrations,
// re-applied here so a virgin log.db comes up with the right shape.
// Mirrors the db.go migrations slice byte-for-byte for those tables so
// future changes can be diffed cleanly.
func (d *LogDB) applySchema() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS "LogLine" (
			"id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			"ts" DATETIME NOT NULL,
			"pod" TEXT NOT NULL,
			"project" TEXT NOT NULL DEFAULT '',
			"service" TEXT NOT NULL DEFAULT '',
			"env" TEXT NOT NULL DEFAULT '',
			"line" TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS "LogLine_project_service_ts_idx" ON "LogLine"("project","service","ts" DESC)`,
		`CREATE INDEX IF NOT EXISTS "LogLine_ts_idx" ON "LogLine"("ts")`,
		`CREATE VIRTUAL TABLE IF NOT EXISTS "LogLineFts" USING fts5(
			line,
			content='LogLine',
			content_rowid='id',
			tokenize='porter unicode61'
		)`,
		`CREATE TRIGGER IF NOT EXISTS LogLine_ai AFTER INSERT ON "LogLine" BEGIN
			INSERT INTO LogLineFts(rowid, line) VALUES (new.id, new.line);
		END`,
		`CREATE TRIGGER IF NOT EXISTS LogLine_ad AFTER DELETE ON "LogLine" BEGIN
			INSERT INTO LogLineFts(LogLineFts, rowid, line) VALUES('delete', old.id, old.line);
		END`,
	}
	for _, s := range stmts {
		if _, err := d.DB.Exec(s); err != nil {
			msg := err.Error()
			if strings.Contains(msg, "already exists") || strings.Contains(msg, "duplicate column name") {
				continue
			}
			return fmt.Errorf("logdb: apply schema %q: %w", s, err)
		}
	}
	return nil
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

// SearchLogs runs the search. Returns newest-first. Same query
// shapes as the legacy *DB implementation:
//   - empty req.Query → straight LogLine scan
//   - non-empty req.Query → FTS5 MATCH joined back to LogLine
func (d *LogDB) SearchLogs(ctx context.Context, req SearchLogsRequest) ([]LogLine, error) {
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
// given window for a given query? Empty query counts every line
// (pod-volume alerting).
func (d *LogDB) CountLogMatches(ctx context.Context, project, service, query string, since time.Time) (int, error) {
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
func (d *LogDB) PruneLogsOlderThan(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.DB.ExecContext(ctx, `DELETE FROM "LogLine" WHERE "ts" < ?`, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("prune logs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
