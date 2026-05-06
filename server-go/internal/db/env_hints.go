package db

import (
	"context"
	"fmt"
	"time"
)

// EnvHint is one (project, service, name) → "we saw a crash mention
// this var" tuple. Upserted by the log shipper's crash-line scanner;
// queried by the UI to flag missing env vars on the EnvVarsEditor.
type EnvHint struct {
	Project  string
	Service  string
	Name     string
	LastLine string
	LastSeen time.Time
}

// UpsertEnvHints batches a slice of hints into the EnvHint table.
// Conflict on (project, service, name) refreshes lastLine + lastSeen
// so a recent crash overwrites a stale one and the UI shows the
// most-recent context. Empty input is a no-op (the shipper calls
// this every flushInterval whether anything was matched or not).
func (d *DB) UpsertEnvHints(ctx context.Context, hints []EnvHint) error {
	if len(hints) == 0 {
		return nil
	}
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("db: env hints begin: %w", err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `
INSERT INTO "EnvHint" ("project","service","name","lastLine","lastSeen")
VALUES (?, ?, ?, ?, ?)
ON CONFLICT ("project","service","name") DO UPDATE SET
  "lastLine" = EXCLUDED."lastLine",
  "lastSeen" = EXCLUDED."lastSeen"`)
	if err != nil {
		return fmt.Errorf("db: env hints prepare: %w", err)
	}
	defer stmt.Close()
	for _, h := range hints {
		if _, err := stmt.ExecContext(ctx, h.Project, h.Service, h.Name, h.LastLine, h.LastSeen.UTC()); err != nil {
			return fmt.Errorf("db: env hints exec: %w", err)
		}
	}
	return tx.Commit()
}

// ListEnvHints returns the recent hints for a (project, service) pair.
// Newest first, capped at 50 (the UI shows a small badge — beyond that
// the list is noise).
func (d *DB) ListEnvHints(ctx context.Context, project, service string) ([]EnvHint, error) {
	rows, err := d.QueryContext(ctx, `
SELECT "project", "service", "name", "lastLine", "lastSeen"
FROM "EnvHint"
WHERE "project" = ? AND "service" = ?
ORDER BY "lastSeen" DESC
LIMIT 50`, project, service)
	if err != nil {
		return nil, fmt.Errorf("db: list env hints: %w", err)
	}
	defer rows.Close()
	out := []EnvHint{}
	for rows.Next() {
		var h EnvHint
		if err := rows.Scan(&h.Project, &h.Service, &h.Name, &h.LastLine, &h.LastSeen); err != nil {
			return nil, fmt.Errorf("db: scan env hint: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// DeleteEnvHint removes a single hint — called when the user adds the
// var (so the badge clears immediately) or dismisses it explicitly.
func (d *DB) DeleteEnvHint(ctx context.Context, project, service, name string) error {
	_, err := d.ExecContext(ctx, `
DELETE FROM "EnvHint" WHERE "project" = ? AND "service" = ? AND "name" = ?`,
		project, service, name)
	if err != nil {
		return fmt.Errorf("db: delete env hint: %w", err)
	}
	return nil
}
