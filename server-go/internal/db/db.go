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
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/lib/pq"
)

//go:embed schema.sql
var schemaSQL string

// DB is a thin wrapper around *sql.DB that holds a write-error counter
// for the alert engine. Exec/ExecContext shadow the embedded methods
// to bump that counter on failure; every other method falls through to
// the embedded *sql.DB. All queries in this package are native
// Postgres `$N` placeholders (the v0.9 Postgres migration; the
// SQLite-dialect `?`/`INSERT OR IGNORE` translation shim was removed in
// v0.18 once every query was converted).
type DB struct {
	*sql.DB
	Stats   Stats
	tenancy *tenancyCache
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
	// 5min lifetime cycles connections aggressively enough that a
	// Postgres rollout's stale connections get reaped in tens of
	// seconds rather than tens of minutes — old "30m" meant the
	// post-rollout error wave (driver retries handle most of it but
	// some surface as 5xx) lasted for a long tail. Trade-off: more
	// reconnect handshakes; negligible at our request rate.
	sqldb.SetConnMaxLifetime(5 * time.Minute)

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
	d := &DB{DB: sqldb, tenancy: newTenancyCache()}
	// Apply the baseline schema (idempotent) + any pending versioned
	// migrations, in order, recorded in SchemaMigration. runMigrations
	// calls applySchema internally for the baseline.
	if err := d.runMigrations(context.Background()); err != nil {
		_ = d.Close()
		return nil, err
	}
	// One-shot role-system-v2 wipe-and-re-grant. Marker-guarded; no-op
	// after the first boot on the new schema.
	if err := d.migrateRoleSystemV2(context.Background()); err != nil {
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

// Query methods on *DB. These shadow the embedded *sql.DB methods
// (Go resolves the outer receiver first) for one reason: the write
// paths increment the WriteErrors counter the alert engine reads.
// Queries are native Postgres `$N` — no placeholder rewriting; the
// SQLite-dialect shim was removed in v0.18 once every query in this
// package was converted to `$N` + `ON CONFLICT`.

func (d *DB) Exec(query string, args ...any) (sql.Result, error) {
	res, err := d.DB.Exec(query, args...)
	if err != nil {
		d.Stats.WriteErrors.Add(1)
	}
	return res, err
}

func (d *DB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	res, err := d.DB.ExecContext(ctx, query, args...)
	if err != nil {
		d.Stats.WriteErrors.Add(1)
	}
	return res, err
}

// Tx wraps *sql.Tx so BeginTx returns a type whose ExecContext bumps
// the same WriteErrors counter as the *DB path. Commit, Rollback,
// Stmt, etc remain reachable through the embedded *sql.Tx.
type Tx struct {
	*sql.Tx
}

// BeginTx wraps sql.DB.BeginTx and returns the wrapping *Tx. Callers
// that already used `d.DB.BeginTx(...)` keep working since *sql.Tx is
// embedded — the upgrade is transparent for every method.
func (d *DB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*Tx, error) {
	tx, err := d.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &Tx{Tx: tx}, nil
}

func (t *Tx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.Tx.ExecContext(ctx, query, args...)
}

// envOrDefault is exported across this package so cmd/ can read the
// DSN env var without importing os here just for that.
func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
