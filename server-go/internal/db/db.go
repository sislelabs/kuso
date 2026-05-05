// Package db wraps the kuso Postgres database.
//
// The original SQLite-backed schema (Prisma-generated) was replaced
// in v0.9 with a Postgres-native version. The dialect change unlocks:
//
//   - HA: kuso-server can run replicas behind a leader-elected
//     poller. Postgres handles concurrent writes; we don't pin a
//     writer.
//   - Zero-downtime upgrades: the deploy yaml uses RollingUpdate
//     (no more Recreate / RWO PVC).
//   - Higher write throughput: multi-conn replaces SetMaxOpenConns(1).
//
// Driver: github.com/lib/pq. DSN comes from KUSO_DB_DSN. CGO stays
// off; the kuso-server image remains a static binary.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schemaSQL string

// DB is a thin wrapper around *sql.DB that:
//
//  1. Holds a write-error counter for the alert engine.
//  2. Rewrites SQLite-flavour `?` placeholders to Postgres `$N` so
//     the per-resource helper files (users_crud.go, tokens.go, ...)
//     can keep their existing query strings.
//
// Callers continue to use d.QueryRowContext / d.ExecContext /
// d.QueryContext as if it were a *sql.DB. The methods are shadowed
// here; embedded *sql.DB's matching methods stay reachable as
// d.DB.<method> if a caller ever needs the raw passthrough.
type DB struct {
	*sql.DB
	Stats Stats
}

// Open opens a Postgres connection. dsn is the libpq URI (e.g.
// "postgres://kuso:secret@kuso-postgres:5432/kuso?sslmode=disable").
// Idempotent — applies the embedded schema on every Open so a fresh
// install gets the full shape and an existing one is left intact.
func Open(dsn string) (*DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("db: empty DSN (set KUSO_DB_DSN)")
	}
	sqldb, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	// Connection pool. Postgres handles concurrent writes; cap at 25
	// so we don't exhaust the per-database max_connections (default
	// 100, shared with operator + addons).
	sqldb.SetMaxOpenConns(25)
	sqldb.SetMaxIdleConns(5)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)
	sqldb.SetConnMaxLifetime(30 * time.Minute)

	// Boot ping with retry — Postgres pod might still be starting on
	// a fresh install. 10 second total budget; readiness probe wins
	// after that.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err = sqldb.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = sqldb.Close()
			return nil, fmt.Errorf("db: ping: %w", err)
		}
		time.Sleep(500 * time.Millisecond)
	}
	d := &DB{DB: sqldb}
	if err := d.applySchema(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// Stats holds runtime counters for the DB layer. Read by the alert
// engine and exposed on /api/stats.
type Stats struct {
	WriteErrors atomic.Uint64
}

// applySchema executes schema.sql one statement at a time. lib/pq's
// simple-query path doesn't reliably handle multi-statement strings,
// so we split on `;` ourselves.
func (d *DB) applySchema() error {
	for _, stmt := range splitSQL(schemaSQL) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := d.DB.Exec(stmt); err != nil {
			// IF NOT EXISTS catches most re-runs; tolerate the
			// remaining edge cases (constraint already present,
			// duplicate column under ALTER … ADD COLUMN even with
			// the IF NOT EXISTS clause).
			msg := err.Error()
			if strings.Contains(msg, "already exists") ||
				strings.Contains(msg, "duplicate column") {
				continue
			}
			return fmt.Errorf("db: apply schema: %w\n  stmt: %s", err, stmt)
		}
	}
	return nil
}

// splitSQL splits a multi-statement SQL blob on `;` while respecting
// single-quoted strings, double-quoted identifiers, and `--` line
// comments. Comments are stripped (not preserved) so an apostrophe
// inside an English-prose comment can't toggle the splitter into
// phantom-string-literal mode and swallow `;` boundaries.
//
// Doesn't handle plpgsql DO $$ ... $$ blocks. schema.sql doesn't use
// them; if it ever does, swap this for a real parser.
func splitSQL(in string) []string {
	out := []string{}
	var cur strings.Builder
	inSingle, inDouble, inLineComment := false, false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inLineComment {
			// Drop the comment body; just track when it ends.
			if c == '\n' {
				inLineComment = false
				cur.WriteByte(c) // keep the newline so statement
				// boundaries (whitespace) are preserved.
			}
			continue
		}
		// Line comment start: `--` outside of any string. Skip the
		// `--` itself (don't write); inLineComment will eat the rest.
		if !inSingle && !inDouble && c == '-' && i+1 < len(in) && in[i+1] == '-' {
			inLineComment = true
			i++ // consume the second `-`
			continue
		}
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			cur.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			cur.WriteByte(c)
		case c == ';' && !inSingle && !inDouble:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

// rewriteQuery converts SQLite `?` placeholders to Postgres `$N`,
// and rewrites the SQLite-only `INSERT OR IGNORE` / `INSERT OR REPLACE`
// shapes to their Postgres equivalents.
//
// Strings and quoted identifiers mask the `?` so literal occurrences
// (e.g. JSON `'a?b'`) are preserved. Hot path on every query —
// keep it allocation-light.
func rewriteQuery(q string) string {
	q = rewriteUpsert(q)
	if !strings.Contains(q, "?") {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	inSingle, inDouble := false, false
	n := 1
	for i := 0; i < len(q); i++ {
		c := q[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			b.WriteByte(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			b.WriteByte(c)
		case c == '?' && !inSingle && !inDouble:
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			n++
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// rewriteUpsert turns SQLite-only INSERT shapes into Postgres ones:
//
//   INSERT OR IGNORE INTO X (...)  →  INSERT INTO X (...) ... ON CONFLICT DO NOTHING
//   INSERT OR REPLACE INTO X (...) →  INSERT INTO X (...) ... ON CONFLICT DO UPDATE SET ...
//
// The IGNORE form is trivial — append the ON CONFLICT clause if not
// already present and strip the OR-clause. The REPLACE form is more
// invasive (Postgres needs an explicit conflict target column), so
// we don't transform it here; any caller using OR REPLACE has been
// rewritten manually to ON CONFLICT(...) DO UPDATE SET when needed.
//
// Case-insensitive on the leading INSERT keyword so callers that
// prefer lowercase still match.
func rewriteUpsert(q string) string {
	trimmed := strings.TrimLeft(q, " \t\r\n")
	upper := strings.ToUpper(trimmed)
	switch {
	case strings.HasPrefix(upper, "INSERT OR IGNORE INTO"):
		// Replace just the OR-IGNORE prefix; the rest of the query is
		// unchanged. Append ON CONFLICT DO NOTHING if no ON CONFLICT
		// clause already exists.
		idx := strings.Index(q, "INSERT OR IGNORE INTO")
		if idx < 0 {
			// Lowercase / mixed case — find via case-insensitive scan.
			idx = caseInsensitiveIndex(q, "insert or ignore into")
		}
		if idx < 0 {
			return q
		}
		out := q[:idx] + "INSERT INTO" + q[idx+len("INSERT OR IGNORE INTO"):]
		if !strings.Contains(strings.ToUpper(out), "ON CONFLICT") {
			out = strings.TrimRight(out, " \t\r\n;") + " ON CONFLICT DO NOTHING"
		}
		return out
	default:
		return q
	}
}

// caseInsensitiveIndex finds the first occurrence of needle in
// haystack ignoring ASCII case. Cheap (no regex). Used by
// rewriteUpsert to handle queries written in lowercase.
func caseInsensitiveIndex(haystack, needle string) int {
	if len(needle) == 0 {
		return 0
	}
	hl := strings.ToLower(haystack)
	nl := strings.ToLower(needle)
	return strings.Index(hl, nl)
}

// Shadowed query methods — these win over *sql.DB's via Go method
// resolution because they're defined on the outer *DB receiver. The
// embedded methods remain reachable as d.DB.QueryContext if a caller
// needs to pass a literal `?` through.

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	res, err := d.DB.Exec(rewriteQuery(query), args...)
	if err != nil {
		d.Stats.WriteErrors.Add(1)
	}
	return res, err
}

func (d *DB) Query(query string, args ...any) (*sql.Rows, error) {
	return d.DB.Query(rewriteQuery(query), args...)
}

func (d *DB) QueryRow(query string, args ...any) *sql.Row {
	return d.DB.QueryRow(rewriteQuery(query), args...)
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := d.DB.ExecContext(ctx, rewriteQuery(query), args...)
	if err != nil {
		d.Stats.WriteErrors.Add(1)
	}
	return res, err
}

func (d *DB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return d.DB.QueryContext(ctx, rewriteQuery(query), args...)
}

func (d *DB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return d.DB.QueryRowContext(ctx, rewriteQuery(query), args...)
}

// Tx is the same kind of wrapper as *DB but for transactions. Begin /
// BeginTx return *Tx so the placeholder rewriter applies to every
// query inside a transaction too. Without this, an INSERT inside a
// tx (bootstrap, group reassignment, role replace) would receive
// raw `?` placeholders that Postgres rejects.
//
// We only wrap the placeholder-using methods; Commit, Rollback,
// Stmt, etc are still reachable through the embedded *sql.Tx.
type Tx struct {
	*sql.Tx
}

// BeginTx wraps sql.DB.BeginTx and returns the wrapping *Tx. Callers
// that already used `d.DB.BeginTx(...)` keep working since *sql.Tx is
// embedded — the upgrade is transparent for every method that doesn't
// take a query string.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx}, nil
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, rewriteQuery(query), args...)
}

func (t *Tx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.Tx.QueryContext(ctx, rewriteQuery(query), args...)
}

func (t *Tx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.Tx.QueryRowContext(ctx, rewriteQuery(query), args...)
}

// PrepareContext returns a *sql.Stmt against the *rewritten* query.
// Callers that want to keep `?` placeholders inside Prepare get the
// rewrite for free; native-Postgres callers passing $1 already work.
func (t *Tx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.Tx.PrepareContext(ctx, rewriteQuery(query))
}

// envOrDefault is exported across this package so cmd/ can read the
// DSN env var without importing os here just for that.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
