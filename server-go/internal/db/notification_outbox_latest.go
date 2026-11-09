package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// OutboxLatest is the newest outbox row for one (event type, alert rule,
// channel). RuleID is set only for alert.fired rows, read from the
// payload's extra.rule_id, so each alert rule gets its own result.
type OutboxLatest struct {
	EventType      string
	RuleID         string
	NotificationID string
	Attempts       int
	LastError      string
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

// LatestOutboxDeliveries reads the last delivery per event type (and per
// alert rule) and channel from the outbox. Delivered rows are pruned after
// KUSO_NOTIFY_RETENTION_DAYS, so an event that hasn't fired inside that
// window has no row.
func (d *DB) LatestOutboxDeliveries(ctx context.Context) ([]OutboxLatest, error) {
	rows, err := d.QueryContext(ctx, `
		SELECT DISTINCT ON ("eventType", rid, "notificationId")
			"eventType", rid, "notificationId", "attempts", COALESCE("lastError", ''), "deliveredAt", "createdAt"
		FROM (
			SELECT *, COALESCE("payload"->'extra'->>'rule_id', '') AS rid
			FROM "NotificationOutbox"
		) o
		ORDER BY "eventType", rid, "notificationId", "id" DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("latest outbox deliveries: %w", err)
	}
	defer rows.Close()
	out := []OutboxLatest{}
	for rows.Next() {
		var r OutboxLatest
		var delivered sql.NullTime
		if err := rows.Scan(&r.EventType, &r.RuleID, &r.NotificationID, &r.Attempts, &r.LastError, &delivered, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan latest outbox delivery: %w", err)
		}
		if delivered.Valid {
			t := delivered.Time
			r.DeliveredAt = &t
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("latest outbox deliveries: %w", err)
	}
	return out, nil
}
