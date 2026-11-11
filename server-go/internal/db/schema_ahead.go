package db

import (
	"context"
	"fmt"
)

// SchemaAhead reports when the database carries versioned migrations this
// binary doesn't know: the state after the updater reverts a failed
// upgrade whose new server already migrated. Additive migrations leave the
// old binary working, but nothing guarantees that, so the revert must not
// read as a clean "reverted to working". Returns "" when the binary knows
// every applied migration.
func (d *DB) SchemaAhead(ctx context.Context) (string, error) {
	migs, err := loadMigrations()
	if err != nil {
		return "", err
	}
	known := 0
	for _, m := range migs {
		known = max(known, m.version)
	}
	var applied int
	if err := d.QueryRowContext(ctx, `SELECT COALESCE(MAX("version"), 0) FROM "SchemaMigration"`).Scan(&applied); err != nil {
		return "", fmt.Errorf("db: read applied migrations: %w", err)
	}
	return schemaAheadMessage(applied, known), nil
}

func schemaAheadMessage(applied, known int) string {
	if applied <= known {
		return ""
	}
	return fmt.Sprintf("database is at migration %04d but this server only knows up to %04d (a newer kuso version migrated it)", applied, known)
}
