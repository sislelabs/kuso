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
	if err := d.applyMigrations(); err != nil {
		_ = d.Close()
		return nil, err
	}
	return d, nil
}

// applyMigrations runs additive ALTER TABLE statements that aren't in
// schema.sql (the Prisma dump). Each is wrapped in a tolerant exec —
// SQLite ALTER TABLE ADD COLUMN errors with "duplicate column name"
// when re-applied, which we silence so this is idempotent. New
// migrations append to the slice; never reorder or remove (operators
// running an old kuso instance need a stable replay sequence).
func (d *DB) applyMigrations() error {
	migrations := []string{
		// v0.5: tenancy. Each Group carries an instance-wide role
		// (admin/member/viewer/billing/pending) and a JSON list of
		// per-project memberships [{project, role}].
		`ALTER TABLE "UserGroup" ADD COLUMN "instanceRole" TEXT NOT NULL DEFAULT 'member'`,
		`ALTER TABLE "UserGroup" ADD COLUMN "projectMemberships" TEXT NOT NULL DEFAULT '[]'`,
		// v0.6.10: invitation links. Admin mints a token, the URL is
		// shared, the invitee redeems it through GH OAuth or local
		// signup. groupId is nullable so an admin can mint a generic
		// invite that lands in the pending group; instanceRole
		// nullable means "use the group's default."
		`CREATE TABLE IF NOT EXISTS "Invite" (
			"id" TEXT PRIMARY KEY,
			"token" TEXT NOT NULL UNIQUE,
			"groupId" TEXT,
			"instanceRole" TEXT,
			"createdBy" TEXT NOT NULL,
			"createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			"expiresAt" DATETIME,
			"maxUses" INTEGER NOT NULL DEFAULT 1,
			"usedCount" INTEGER NOT NULL DEFAULT 0,
			"revokedAt" DATETIME,
			"note" TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS "Invite_token_idx" ON "Invite"("token")`,
		// Audit trail of who used which invite. usedAt + userId per
		// row — keeps revoke/audit decisions easy and survives a
		// User row delete via cascade.
		`CREATE TABLE IF NOT EXISTS "InviteRedemption" (
			"id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			"inviteId" TEXT NOT NULL,
			"userId" TEXT NOT NULL,
			"usedAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY ("inviteId") REFERENCES "Invite"("id") ON DELETE CASCADE,
			FOREIGN KEY ("userId") REFERENCES "User"("id") ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS "InviteRedemption_inviteId_idx" ON "InviteRedemption"("inviteId")`,
		// v0.6.22: node metrics history. The kuso server samples every
		// kube node's CPU/RAM/disk every 30 min via metrics-server +
		// node status (allocatable/capacity/availableBytes), drops
		// rows older than 7 days, and renders them as sparklines on
		// the settings/nodes drill-down. SQLite-backed because the
		// sample volume is trivial — 1 row × 30 min × 7d × N nodes —
		// and adding a real TSDB to a one-box install isn't worth it.
		`CREATE TABLE IF NOT EXISTS "NodeMetric" (
			"id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			"node" TEXT NOT NULL,
			"ts" DATETIME NOT NULL,
			"cpuUsedMilli" INTEGER NOT NULL DEFAULT 0,
			"cpuCapacityMilli" INTEGER NOT NULL DEFAULT 0,
			"memUsedBytes" INTEGER NOT NULL DEFAULT 0,
			"memCapacityBytes" INTEGER NOT NULL DEFAULT 0,
			"diskAvailBytes" INTEGER NOT NULL DEFAULT 0,
			"diskCapacityBytes" INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS "NodeMetric_node_ts_idx" ON "NodeMetric"("node","ts")`,
		`CREATE INDEX IF NOT EXISTS "NodeMetric_ts_idx" ON "NodeMetric"("ts")`,
	}
	for _, sqlText := range migrations {
		if _, err := d.DB.Exec(sqlText); err != nil {
			// Idempotent re-apply: "duplicate column name" for ALTER
			// TABLE … ADD COLUMN, "already exists" for CREATE TABLE
			// IF NOT EXISTS guards (the IF NOT EXISTS catches most
			// but newer SQLite versions still surface the message in
			// some constraints). Anything else is a real failure.
			msg := err.Error()
			if strings.Contains(msg, "duplicate column name") ||
				strings.Contains(msg, "already exists") {
				continue
			}
			return fmt.Errorf("db: migration %q: %w", sqlText, err)
		}
	}
	return nil
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
