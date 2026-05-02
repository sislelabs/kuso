// Package db wraps the kuso SQLite database. Schema is the same on-disk
// shape Prisma emits today; we open the existing file without migration
// (see kuso/docs/REWRITE.md §4a).
//
// Pure-Go driver: modernc.org/sqlite. CGO stays off so the kuso-server
// image remains a static scratch binary.
package db

import (
	"database/sql"
	_ "embed"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// DB is a thin wrapper around *sql.DB that exposes typed query helpers
// per resource. Queries live in resource-specific files (users.go,
// tokens.go, ...).
type DB struct {
	*sql.DB
}

// Open opens (or creates) the SQLite database at path and ensures the
// schema is applied. Idempotent — safe to call against a live database
// that already has every table.
//
// dsn is the raw SQLite path (e.g. "/var/lib/kuso/kuso.db"). The pragma
// suffix forces foreign-key enforcement and busy-timeout for low-contention
// concurrency, matching what Prisma's runtime sets.
func Open(path string) (*DB, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open %s: %w", path, err)
	}
	// SQLite does not benefit from many writers; cap the pool to one for
	// writes, which the WAL pragma + busy-timeout already implies.
	sqldb.SetMaxOpenConns(1)
	sqldb.SetConnMaxIdleTime(5 * time.Minute)

	if err := sqldb.Ping(); err != nil {
		return nil, fmt.Errorf("db: ping %s: %w", path, err)
	}
	d := &DB{DB: sqldb}
	if err := d.applySchema(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// applySchema runs the embedded Prisma DDL with each CREATE TABLE switched
// to CREATE TABLE IF NOT EXISTS (and similarly for indexes). This makes
// the call idempotent so an existing kuso.db is left intact while a fresh
// one gets the full schema.
func (d *DB) applySchema() error {
	sqlText := rewriteCreateAsIfNotExists(schemaSQL)
	if _, err := d.DB.Exec(sqlText); err != nil {
		return fmt.Errorf("db: apply schema: %w", err)
	}
	return nil
}

// rewriteCreateAsIfNotExists is a tiny string transform — Prisma emits
// `CREATE TABLE "X"` and `CREATE [UNIQUE] INDEX "X"` without IF NOT EXISTS.
// Hand-rolling the rewrite keeps schema.sql byte-identical to Prisma's
// output, which makes future re-dumps a clean diff.
func rewriteCreateAsIfNotExists(s string) string {
	rep := strings.NewReplacer(
		"CREATE TABLE \"", "CREATE TABLE IF NOT EXISTS \"",
		"CREATE UNIQUE INDEX \"", "CREATE UNIQUE INDEX IF NOT EXISTS \"",
		"CREATE INDEX \"", "CREATE INDEX IF NOT EXISTS \"",
	)
	return rep.Replace(s)
}

// BackupTo creates a consistent snapshot of the database at dst using
// SQLite's VACUUM INTO. The destination file MUST NOT exist (SQLite
// errors otherwise). Other connections continue writing during the
// backup — VACUUM INTO is online and atomic.
//
// Caller is responsible for cleanup of dst on error and for streaming
// the resulting file to the client.
func (d *DB) BackupTo(dst string) error {
	// VACUUM INTO uses a literal — passing dst via parameter binding
	// raises "no such table". Quote it explicitly so a path with single
	// quotes can't break out (we replace ' with '' SQL-style).
	quoted := strings.ReplaceAll(dst, "'", "''")
	if _, err := d.DB.Exec("VACUUM INTO '" + quoted + "'"); err != nil {
		return fmt.Errorf("db: backup to %q: %w", dst, err)
	}
	return nil
}
