// Per-project notification mutes. A row = the project's events skip
// external channel delivery (Discord/Slack/webhook/email/telegram/
// pushover). The bell-feed mirror (NotificationEvent) is deliberately
// unaffected so the in-app audit trail survives a mute. Absence of a
// row = not muted, which keeps "unmute everything" a plain DELETE and
// the dispatcher read a small full-table scan. See migration 0008.

package db

import (
	"context"
	"fmt"
	"time"
)

// ProjectNotificationMute is one muted project.
type ProjectNotificationMute struct {
	Project   string    `json:"project"`
	CreatedAt time.Time `json:"createdAt"`
	CreatedBy string    `json:"createdBy,omitempty"`
}

// ListProjectNotificationMutes returns every muted project, oldest
// first. Empty slice (not nil) when nothing is muted.
func (d *DB) ListProjectNotificationMutes(ctx context.Context) ([]ProjectNotificationMute, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT "project","createdAt","createdBy"
		FROM "ProjectNotificationMute"
		ORDER BY "createdAt" ASC`)
	if err != nil {
		return nil, fmt.Errorf("list project notification mutes: %w", err)
	}
	defer rows.Close()
	out := []ProjectNotificationMute{}
	for rows.Next() {
		var m ProjectNotificationMute
		var created prismaTime
		if err := rows.Scan(&m.Project, &created, &m.CreatedBy); err != nil {
			return nil, fmt.Errorf("scan project notification mute: %w", err)
		}
		m.CreatedAt = created.Time
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// SetProjectNotificationMute mutes a project. Idempotent — re-muting an
// already-muted project keeps the original row (first muter + time win,
// so the audit answer to "since when?" stays stable).
func (d *DB) SetProjectNotificationMute(ctx context.Context, project, by string) error {
	_, err := d.ExecContext(ctx, `
		INSERT INTO "ProjectNotificationMute" ("project","createdBy")
		VALUES ($1,$2)
		ON CONFLICT ("project") DO NOTHING`,
		project, by)
	if err != nil {
		return fmt.Errorf("mute project notifications: %w", err)
	}
	return nil
}

// ClearProjectNotificationMute unmutes a project. Idempotent.
func (d *DB) ClearProjectNotificationMute(ctx context.Context, project string) error {
	_, err := d.ExecContext(ctx, `
		DELETE FROM "ProjectNotificationMute" WHERE "project" = $1`, project)
	if err != nil {
		return fmt.Errorf("unmute project notifications: %w", err)
	}
	return nil
}
