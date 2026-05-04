package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notification mirrors the on-disk Notification row plus a typed config
// payload derived from the type-specific columns.
type Notification struct {
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Enabled       bool           `json:"enabled"`
	Type          string         `json:"type"`
	Pipelines     []string       `json:"pipelines"`
	Events        []string       `json:"events"`
	Config        map[string]any `json:"config"`
	CreatedAt     time.Time      `json:"createdAt"`
	UpdatedAt     time.Time      `json:"updatedAt"`
}

// ListNotifications returns every notification config.
func (d *DB) ListNotifications(ctx context.Context) ([]Notification, error) {
	rows, err := d.DB.QueryContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "createdAt", "updatedAt"
FROM "Notification" ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("db: list notifications: %w", err)
	}
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// FindNotification returns one notification by id, or ErrNotFound.
func (d *DB) FindNotification(ctx context.Context, id string) (*Notification, error) {
	row := d.DB.QueryRowContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "createdAt", "updatedAt"
FROM "Notification" WHERE id = ?`, id)
	n, err := scanNotification(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return n, err
}

// CreateNotification inserts a new row.
func (d *DB) CreateNotification(ctx context.Context, n *Notification) error {
	if n.ID == "" || n.Name == "" || n.Type == "" {
		return errors.New("db: id, name, type required")
	}
	pj, _ := json.Marshal(coalesceStringSlice(n.Pipelines))
	ej, _ := json.Marshal(coalesceStringSlice(n.Events))
	now := prismaNow()
	cfg := configCols(n.Type, n.Config)
	_, err := d.ExecContext(ctx, `
INSERT INTO "Notification" (id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "createdAt", "updatedAt")
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		n.ID, n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("db: create notification: %w", err)
	}
	n.CreatedAt, n.UpdatedAt = now.Time, now.Time
	return nil
}

// UpdateNotification replaces a row's fields. Wholesale replace (not
// partial) — the TS controller already accepts the full new config on
// PUT, and a partial path here would have to know how to clear the
// type-specific fields when type changes.
func (d *DB) UpdateNotification(ctx context.Context, n *Notification) error {
	pj, _ := json.Marshal(coalesceStringSlice(n.Pipelines))
	ej, _ := json.Marshal(coalesceStringSlice(n.Events))
	cfg := configCols(n.Type, n.Config)
	res, err := d.ExecContext(ctx, `
UPDATE "Notification" SET name = ?, enabled = ?, type = ?, pipelines = ?, events = ?,
  "webhookUrl" = ?, "webhookSecret" = ?, "slackUrl" = ?, "slackChannel" = ?, "discordUrl" = ?,
  "updatedAt" = ?
WHERE id = ?`,
		n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		prismaNow(), n.ID,
	)
	if err != nil {
		return fmt.Errorf("db: update notification: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteNotification removes a row.
func (d *DB) DeleteNotification(ctx context.Context, id string) error {
	res, err := d.ExecContext(ctx, `DELETE FROM "Notification" WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete notification: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

// scanNotification decodes one row into a Notification.
func scanNotification(s interface {
	Scan(...any) error
}) (*Notification, error) {
	var n Notification
	var pipelines, events string
	var webhookURL, webhookSecret, slackURL, slackChannel, discordURL sql.NullString
	var createdAt, updatedAt prismaTime
	if err := s.Scan(
		&n.ID, &n.Name, &n.Enabled, &n.Type, &pipelines, &events,
		&webhookURL, &webhookSecret, &slackURL, &slackChannel, &discordURL,
		&createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	n.CreatedAt = createdAt.Time
	n.UpdatedAt = updatedAt.Time
	_ = json.Unmarshal([]byte(pipelines), &n.Pipelines)
	_ = json.Unmarshal([]byte(events), &n.Events)
	n.Config = map[string]any{}
	switch n.Type {
	case "webhook":
		if webhookURL.Valid {
			n.Config["url"] = webhookURL.String
		}
		if webhookSecret.Valid {
			n.Config["secret"] = webhookSecret.String
		}
	case "slack":
		if slackURL.Valid {
			n.Config["url"] = slackURL.String
		}
		if slackChannel.Valid {
			n.Config["channel"] = slackChannel.String
		}
	case "discord":
		if discordURL.Valid {
			n.Config["url"] = discordURL.String
		}
	}
	return &n, nil
}

type notifConfigCols struct {
	webhookURL    sql.NullString
	webhookSecret sql.NullString
	slackURL      sql.NullString
	slackChannel  sql.NullString
	discordURL    sql.NullString
}

func configCols(typ string, cfg map[string]any) notifConfigCols {
	out := notifConfigCols{}
	if cfg == nil {
		return out
	}
	getString := func(k string) sql.NullString {
		v, ok := cfg[k].(string)
		if !ok || v == "" {
			return sql.NullString{}
		}
		return sql.NullString{String: v, Valid: true}
	}
	switch typ {
	case "webhook":
		out.webhookURL = getString("url")
		out.webhookSecret = getString("secret")
	case "slack":
		out.slackURL = getString("url")
		out.slackChannel = getString("channel")
	case "discord":
		out.discordURL = getString("url")
	}
	return out
}

func coalesceStringSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
