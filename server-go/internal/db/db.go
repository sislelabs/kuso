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
//
// stats counts SQLITE_BUSY events on the write path. SQLite + WAL +
// max-open-conns=1 means writes serialize on a single connection;
// a request that loses the busy_timeout race is the leading indicator
// of saturation. See stats.go.
type DB struct {
	*sql.DB
	stats Stats
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
		// v0.6.23: SSH key library for the multi-node "Add node" flow.
		// Stores ed25519/rsa keypairs so the operator can paste the
		// public half into a new VM's authorized_keys and reuse the
		// same key across multiple joins. Coolify-style: keys live
		// independently of servers and are referenced by id.
		`CREATE TABLE IF NOT EXISTS "SSHKey" (
			"id" TEXT PRIMARY KEY,
			"name" TEXT NOT NULL,
			"publicKey" TEXT NOT NULL,
			"privateKey" TEXT NOT NULL,
			"fingerprint" TEXT NOT NULL,
			"createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		// v0.6.29: in-app notification feed. Every event the notify
		// dispatcher fires also lands here so the bell icon in the
		// navbar can render the recent N events. Retention is
		// "last 200 entries"; older rows get pruned on insert.
		`CREATE TABLE IF NOT EXISTS "NotificationEvent" (
			"id" INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			"type" TEXT NOT NULL,
			"title" TEXT NOT NULL,
			"body" TEXT,
			"severity" TEXT NOT NULL DEFAULT 'info',
			"project" TEXT,
			"service" TEXT,
			"url" TEXT,
			"extra" TEXT,
			"createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			"readAt" DATETIME
		)`,
		`CREATE INDEX IF NOT EXISTS "NotificationEvent_createdAt_idx" ON "NotificationEvent"("createdAt" DESC)`,
		`CREATE INDEX IF NOT EXISTS "NotificationEvent_readAt_idx" ON "NotificationEvent"("readAt")`,
		// v0.7: searchable logs. Two tables:
		//  - LogLine: one row per pod log line, ts/pod/project/service/env metadata.
		//  - LogLineFts: FTS5 virtual table mirroring LogLine.line for full-text search.
		// The shipper goroutine streams every pod under the kuso ns
		// into here. Retention is 14d (cron-style prune on the
		// shipper's tick).
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
		// FTS5 contentless table — much smaller on disk, but you have
		// to JOIN back to LogLine to get the metadata. We keep both
		// in sync with simple triggers (insert-only, no update path
		// needed since log lines are immutable once written).
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
		// v0.7: alert rules. Periodic queries over LogLine + NodeMetric
		// that fire notify events when threshold breached.
		// v0.8.5: build log archive. The kaniko Job pod's logs vanish
		// when its TTL elapses (1h after success/failure), but users
		// still want to see why a 3-day-old build failed. The poller
		// snapshots the last 200 lines into here at terminal-phase
		// transition; LogStream falls back to this when the pod is
		// gone. One row per build; logs is a newline-joined string.
		`CREATE TABLE IF NOT EXISTS "BuildLog" (
			"buildName" TEXT PRIMARY KEY,
			"project" TEXT NOT NULL DEFAULT '',
			"service" TEXT NOT NULL DEFAULT '',
			"phase" TEXT NOT NULL DEFAULT '',
			"logs" TEXT NOT NULL DEFAULT '',
			"createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS "BuildLog_project_service_idx" ON "BuildLog"("project","service")`,
		`CREATE TABLE IF NOT EXISTS "AlertRule" (
			"id" TEXT PRIMARY KEY,
			"name" TEXT NOT NULL,
			"enabled" BOOLEAN NOT NULL DEFAULT 1,
			"kind" TEXT NOT NULL,
			"project" TEXT,
			"service" TEXT,
			"query" TEXT NOT NULL DEFAULT '',
			"thresholdInt" INTEGER,
			"thresholdFloat" REAL,
			"windowSeconds" INTEGER NOT NULL DEFAULT 300,
			"severity" TEXT NOT NULL DEFAULT 'warn',
			"throttleSeconds" INTEGER NOT NULL DEFAULT 600,
			"lastFiredAt" DATETIME,
			"createdAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			"updatedAt" DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
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
