// In-app notification feed. Every event the notify dispatcher fires
// is mirrored into this table so the navbar bell can render the
// recent N entries — independent of whether the operator wired up a
// webhook / Discord / Slack sink. Retention is rows-not-time: prune
// the oldest beyond a cap so a noisy cluster doesn't unboundedly
// grow the SQLite file.

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// NotificationEvent is one row in the feed. Mirrors the wire shape
// notify.Event uses, with additional id + createdAt + readAt for the
// UI's "mark read" affordance.
type NotificationEvent struct {
	ID        int64             `json:"id"`
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Body      string            `json:"body,omitempty"`
	Severity  string            `json:"severity,omitempty"`
	Project   string            `json:"project,omitempty"`
	Service   string            `json:"service,omitempty"`
	URL       string            `json:"url,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	ReadAt    *time.Time        `json:"readAt,omitempty"`
}

const notificationEventCap = 200

// InsertNotificationEvent appends one row + prunes anything older
// than the most recent `notificationEventCap` rows. Idempotent
// pruning means the table never exceeds the cap by more than the
// in-flight insert count between transactions (negligible for the
// kuso workload).
func (d *DB) InsertNotificationEvent(ctx context.Context, e NotificationEvent) error {
	extraJSON := ""
	if len(e.Extra) > 0 {
		b, _ := json.Marshal(e.Extra)
		extraJSON = string(b)
	}
	if e.Severity == "" {
		e.Severity = "info"
	}
	if _, err := d.DB.ExecContext(ctx, `
		INSERT INTO "NotificationEvent" ("type","title","body","severity","project","service","url","extra")
		VALUES (?,?,?,?,?,?,?,?)`,
		e.Type, e.Title, e.Body, e.Severity, e.Project, e.Service, e.URL, extraJSON,
	); err != nil {
		return fmt.Errorf("insert notification event: %w", err)
	}
	// Prune everything beyond the cap. The id is monotonically
	// assigned by SQLite so the "newest 200" set is just the highest
	// 200 ids; delete anything below.
	if _, err := d.DB.ExecContext(ctx, `
		DELETE FROM "NotificationEvent"
		WHERE "id" NOT IN (
			SELECT "id" FROM "NotificationEvent" ORDER BY "id" DESC LIMIT ?
		)`, notificationEventCap); err != nil {
		// Pruning failure is non-fatal — the row is in.
		return nil
	}
	return nil
}

// ListNotificationEvents returns the newest `limit` events (clamped
// to the table cap). When unreadOnly is true, only events with
// readAt IS NULL are returned.
func (d *DB) ListNotificationEvents(ctx context.Context, limit int, unreadOnly bool) ([]NotificationEvent, error) {
	if limit <= 0 || limit > notificationEventCap {
		limit = notificationEventCap
	}
	q := `SELECT "id","type","title","body","severity","project","service","url","extra","createdAt","readAt"
	      FROM "NotificationEvent"`
	if unreadOnly {
		q += ` WHERE "readAt" IS NULL`
	}
	q += ` ORDER BY "id" DESC LIMIT ?`
	rows, err := d.DB.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification events: %w", err)
	}
	defer rows.Close()
	out := []NotificationEvent{}
	for rows.Next() {
		var e NotificationEvent
		var body, project, service, url, extra sql.NullString
		var created, read sql.NullTime
		if err := rows.Scan(&e.ID, &e.Type, &e.Title, &body, &e.Severity,
			&project, &service, &url, &extra, &created, &read); err != nil {
			return nil, fmt.Errorf("scan notification event: %w", err)
		}
		e.Body = body.String
		e.Project = project.String
		e.Service = service.String
		e.URL = url.String
		if extra.Valid && extra.String != "" {
			_ = json.Unmarshal([]byte(extra.String), &e.Extra)
		}
		if created.Valid {
			e.CreatedAt = created.Time
		}
		if read.Valid {
			t := read.Time
			e.ReadAt = &t
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountUnreadNotificationEvents is the cheap query the bell icon
// uses to render the unread badge.
func (d *DB) CountUnreadNotificationEvents(ctx context.Context) (int, error) {
	row := d.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM "NotificationEvent" WHERE "readAt" IS NULL`)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

// MarkAllNotificationEventsRead stamps readAt=now() on every unread
// row. Called when the user opens the bell popover.
func (d *DB) MarkAllNotificationEventsRead(ctx context.Context) error {
	_, err := d.DB.ExecContext(ctx, `UPDATE "NotificationEvent" SET "readAt" = CURRENT_TIMESTAMP WHERE "readAt" IS NULL`)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}
