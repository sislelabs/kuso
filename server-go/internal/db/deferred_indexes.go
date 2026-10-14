package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// Indexes on LogLine are built out-of-band instead of inside Open.
//
// LogLine is the one table that grows to tens of millions of rows. A
// plain CREATE INDEX on it inside db.Open (baseline or a versioned
// migration's transaction) runs before the HTTP listener is up, holds a
// SHARE lock that stalls the running pod's log inserts, and can outlast
// the startupProbe: the kubelet kills the pod, the migration tx rolls
// back, and the next boot starts the build from scratch — a crashloop.
//
// BuildDeferredIndexes runs after the listener is up and uses
// CREATE INDEX CONCURRENTLY, which doesn't block writes and can't run in
// a transaction. A deferred migration is recorded in SchemaMigration
// only once its index is valid, so MigrationState shows it pending
// until then.
type deferredIndex struct {
	name string
	// def is everything after `ON "LogLine" `.
	def string
	// migration is the versioned migration this index stands in for, or
	// 0 when it comes from the schema.sql baseline.
	migration int
}

const deferredIndexTable = "LogLine"

var deferredIndexes = []deferredIndex{
	{name: "LogLine_project_service_id_idx", def: `("project", "service", "id" DESC)`, migration: 12},
	{name: "LogLine_line_trgm_idx", def: `USING GIN ("line" gin_trgm_ops)`},
}

// deferredIndexAdvisoryLockKey serialises builders across replicas.
// "kuso_idx" in ASCII.
const deferredIndexAdvisoryLockKey int64 = 0x6b75736f5f696478

func isDeferredMigration(version int) bool {
	for _, ix := range deferredIndexes {
		if ix.migration != 0 && ix.migration == version {
			return true
		}
	}
	return false
}

// BuildDeferredIndexes creates any missing deferred index. Safe to call
// on every boot: a valid index is a cheap catalog lookup; an INVALID one
// (left behind by a cancelled or failed CONCURRENTLY build) is dropped
// and rebuilt. When another replica already holds the build lock this
// returns nil immediately. Errors from one index don't stop the others
// (pg_trgm may be unavailable on managed Postgres).
func (d *DB) BuildDeferredIndexes(ctx context.Context, logger *slog.Logger) error {
	if logger == nil {
		logger = slog.Default()
	}
	conn, err := d.Conn(ctx)
	if err != nil {
		return fmt.Errorf("deferred indexes: acquire conn: %w", err)
	}
	defer conn.Close()

	var locked bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, deferredIndexAdvisoryLockKey).Scan(&locked); err != nil {
		return fmt.Errorf("deferred indexes: advisory lock: %w", err)
	}
	if !locked {
		logger.Info("deferred indexes: another replica is building, skipping")
		return nil
	}
	defer func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, deferredIndexAdvisoryLockKey)
		_, _ = conn.ExecContext(context.Background(), `RESET statement_timeout`)
	}()
	// A big table can take many minutes; don't let a DSN/role-level
	// statement_timeout cancel the build halfway (that leaves it INVALID).
	if _, err := conn.ExecContext(ctx, `SET statement_timeout = 0`); err != nil {
		return fmt.Errorf("deferred indexes: statement_timeout: %w", err)
	}

	var relkind string
	err = conn.QueryRowContext(ctx,
		`SELECT relkind::text FROM pg_class WHERE oid = to_regclass($1)`, quoteIdent(deferredIndexTable)).Scan(&relkind)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("deferred indexes: table %q missing", deferredIndexTable)
	}
	if err != nil {
		return fmt.Errorf("deferred indexes: inspect table: %w", err)
	}
	// Postgres can't build an index CONCURRENTLY on a partitioned
	// parent. MigrateLogLineToPartitioned creates these indexes itself,
	// so this branch only fires if one was dropped by hand.
	partitioned := relkind == "p"

	var errs []error
	for _, ix := range deferredIndexes {
		if err := d.buildDeferredIndex(ctx, conn, ix, partitioned, logger); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", ix.name, err))
		}
	}
	return errors.Join(errs...)
}

func (d *DB) buildDeferredIndex(ctx context.Context, conn *sql.Conn, ix deferredIndex, partitioned bool, logger *slog.Logger) error {
	var valid bool
	err := conn.QueryRowContext(ctx,
		`SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, quoteIdent(ix.name)).Scan(&valid)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// missing → build below
	case err != nil:
		return fmt.Errorf("inspect: %w", err)
	case valid:
		return d.recordDeferredMigration(ctx, conn, ix)
	default:
		logger.Warn("deferred indexes: dropping INVALID index left by an interrupted build", "index", ix.name)
		if _, err := conn.ExecContext(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+quoteIdent(ix.name)); err != nil {
			return fmt.Errorf("drop invalid: %w", err)
		}
	}

	stmt := `CREATE INDEX CONCURRENTLY IF NOT EXISTS ` + quoteIdent(ix.name) + ` ON ` + quoteIdent(deferredIndexTable) + ` ` + ix.def
	if partitioned {
		stmt = `CREATE INDEX IF NOT EXISTS ` + quoteIdent(ix.name) + ` ON ` + quoteIdent(deferredIndexTable) + ` ` + ix.def
	}
	logger.Info("deferred indexes: building", "index", ix.name, "concurrently", !partitioned)
	start := time.Now()
	if _, err := conn.ExecContext(ctx, stmt); err != nil {
		// A failed CONCURRENTLY build leaves an INVALID index; the next
		// call drops and retries it.
		return fmt.Errorf("create: %w", err)
	}
	logger.Info("deferred indexes: built", "index", ix.name, "took", time.Since(start).Round(time.Millisecond))
	return d.recordDeferredMigration(ctx, conn, ix)
}

func (d *DB) recordDeferredMigration(ctx context.Context, conn *sql.Conn, ix deferredIndex) error {
	if ix.migration == 0 {
		return nil
	}
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for _, m := range migs {
		if m.version != ix.migration {
			continue
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT INTO "SchemaMigration" ("version","name","checksum") VALUES ($1,$2,$3)
			 ON CONFLICT ("version") DO NOTHING`,
			m.version, m.name, m.checksum); err != nil {
			return fmt.Errorf("record migration %04d: %w", m.version, err)
		}
		return nil
	}
	return fmt.Errorf("migration %04d not embedded", ix.migration)
}

// quoteIdent double-quotes a constant identifier. Only ever called with
// the compile-time names above.
func quoteIdent(s string) string { return `"` + s + `"` }
