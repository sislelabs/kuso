package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Versioned, ordered, recorded schema migrations.
//
// Why this exists alongside schema.sql: schema.sql is the idempotent
// BASELINE — it CREATE-TABLE-IF-NOT-EXISTS / ALTER-ADD-COLUMN-IF-NOT-
// EXISTS everything, applied on every boot, safe to re-run. It bootstraps
// a fresh DB and brings any existing install up to the current full
// shape. That's good for additive changes but has no version tracking,
// no ordering, and no rollback story — a forward-only change that ISN'T
// expressible as IF-NOT-EXISTS (a data backfill, a column type change, a
// constraint addition, a one-shot transform) has nowhere safe to live.
//
// migrations/NNNN_name.sql files fill that gap: each runs exactly once,
// in version order, inside a transaction, and is recorded in
// SchemaMigration. The runner applies the baseline first (unchanged),
// then any pending numbered migrations. A failed migration aborts boot
// loudly (no IF-NOT-EXISTS swallowing) so a half-applied schema can't go
// unnoticed.
//
// File naming: migrations/NNNN_short_description.sql, NNNN a zero-padded
// monotonic integer (0001, 0002, …). The runner parses NNNN as the
// version; the rest is a human label. One logical change per file.

//go:embed migrations/*.sql
var migrationsFS embed.FS

// migration is one parsed migration file.
type migration struct {
	version  int
	name     string
	body     string
	checksum string // sha256 of body, recorded to detect post-apply edits
}

// runMigrations applies the baseline schema then every pending numbered
// migration in order. Called from Open after the DB connection is live.
func (d *DB) runMigrations(ctx context.Context) error {
	// 1. Baseline: the existing idempotent schema.sql. Unchanged — this
	//    is what fresh installs + already-running clusters rely on.
	if err := d.applySchema(); err != nil {
		return err
	}
	// 2. Bookkeeping table. Created via the baseline path's idempotent
	//    style so it exists before we read it (and on an old DB that
	//    predates it).
	if _, err := d.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS "SchemaMigration" (
			"version"   INTEGER PRIMARY KEY,
			"name"      TEXT NOT NULL,
			"checksum"  TEXT NOT NULL,
			"appliedAt" TIMESTAMPTZ NOT NULL DEFAULT now()
		)`); err != nil {
		return fmt.Errorf("db: ensure SchemaMigration: %w", err)
	}

	// 3. Load + sort the embedded migrations.
	migs, err := loadMigrations()
	if err != nil {
		return err
	}

	// 4. Which versions are already applied?
	applied := map[int]string{} // version → checksum
	rows, err := d.QueryContext(ctx, `SELECT "version","checksum" FROM "SchemaMigration"`)
	if err != nil {
		return fmt.Errorf("db: read applied migrations: %w", err)
	}
	for rows.Next() {
		var v int
		var cs string
		if err := rows.Scan(&v, &cs); err != nil {
			rows.Close()
			return fmt.Errorf("db: scan applied migration: %w", err)
		}
		applied[v] = cs
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// 5. Apply pending, in version order, each in its own tx.
	for _, m := range migs {
		if cs, ok := applied[m.version]; ok {
			// Already applied. Guard against silent edits to a shipped
			// migration — that's a developer error (migrations are
			// immutable once released). Loud, not fatal-by-default:
			// fatal would brick boot on a checksum-only drift, so we
			// fail to keep the operator honest.
			if cs != m.checksum {
				return fmt.Errorf("db: migration %04d (%s) was edited after being applied (checksum %s → %s); migrations are immutable — add a new migration instead", m.version, m.name, cs, m.checksum)
			}
			continue
		}
		if err := d.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("db: migration %04d (%s) failed: %w", m.version, m.name, err)
		}
	}
	return nil
}

// applyMigration runs one migration in a transaction and records it.
// Statement-split mirrors applySchema (lib/pq simple-query can't do
// multi-statement strings); but unlike the baseline we do NOT swallow
// "already exists" — a versioned migration is meant to run exactly once
// against a known state, so any error is real.
func (d *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after Commit

	for _, stmt := range splitSQL(m.body) {
		stmt = strings.TrimSpace(stmt)
		if stmt == "" || strings.HasPrefix(stmt, "--") {
			continue
		}
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("stmt failed (%.80s…): %w", stmt, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO "SchemaMigration" ("version","name","checksum") VALUES ($1,$2,$3)`,
		m.version, m.name, m.checksum); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// loadMigrations reads + parses every embedded migrations/*.sql file,
// sorted ascending by version. Rejects malformed names + duplicate
// versions so a typo can't silently reorder or shadow a migration.
func loadMigrations() ([]migration, error) {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		// No migrations dir embedded (shouldn't happen — the //go:embed
		// would fail at compile) → nothing to apply.
		return nil, nil
	}
	var out []migration
	seen := map[int]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		version, label, perr := parseMigrationName(name)
		if perr != nil {
			return nil, fmt.Errorf("db: migration %q: %w", name, perr)
		}
		if prev, dup := seen[version]; dup {
			return nil, fmt.Errorf("db: duplicate migration version %04d: %q and %q", version, prev, name)
		}
		seen[version] = name
		body, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("db: read migration %q: %w", name, err)
		}
		sum := sha256.Sum256(body)
		out = append(out, migration{
			version:  version,
			name:     label,
			body:     string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// parseMigrationName parses "NNNN_some_label.sql" → (NNNN, "some_label").
func parseMigrationName(filename string) (version int, label string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	i := strings.IndexByte(base, '_')
	if i <= 0 {
		return 0, "", fmt.Errorf("expected NNNN_label.sql")
	}
	v, err := strconv.Atoi(base[:i])
	if err != nil {
		return 0, "", fmt.Errorf("version prefix not an integer: %w", err)
	}
	if v <= 0 {
		return 0, "", fmt.Errorf("version must be >= 1 (0000 is the schema.sql baseline)")
	}
	return v, base[i+1:], nil
}

// MigrationStatus is the applied-migrations summary surfaced to ops
// (and the observability layer in M3).
type MigrationStatus struct {
	BaselineApplied bool   `json:"baselineApplied"`
	Applied         int    `json:"applied"`
	Pending         int    `json:"pending"`
	LatestVersion   int    `json:"latestVersion"`
	LatestName      string `json:"latestName,omitempty"`
}

// MigrationCounts is the metrics-probe view of MigrationState: applied
// + pending, errors swallowed to 0 (a scrape must never fail). Satisfies
// metrics.MigrationProbe without pulling the metrics package in here.
func (d *DB) MigrationCounts() (applied, pending int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	st, err := d.MigrationState(ctx)
	if err != nil {
		return 0, 0
	}
	return st.Applied, st.Pending
}

// MigrationState reports how many migrations are applied vs pending.
// Read-only; cheap (one count + the embedded file list).
func (d *DB) MigrationState(ctx context.Context) (MigrationStatus, error) {
	var st MigrationStatus
	migs, err := loadMigrations()
	if err != nil {
		return st, err
	}
	appliedVersions := map[int]bool{}
	rows, err := d.QueryContext(ctx, `SELECT "version" FROM "SchemaMigration"`)
	if err == nil {
		for rows.Next() {
			var v int
			if rows.Scan(&v) == nil {
				appliedVersions[v] = true
			}
		}
		rows.Close()
	}
	st.BaselineApplied = true // if we got here, applySchema ran at boot
	for _, m := range migs {
		if appliedVersions[m.version] {
			st.Applied++
			if m.version > st.LatestVersion {
				st.LatestVersion, st.LatestName = m.version, m.name
			}
		} else {
			st.Pending++
		}
	}
	return st, nil
}
