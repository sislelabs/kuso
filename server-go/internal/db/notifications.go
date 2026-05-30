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
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	Type      string         `json:"type"`
	Pipelines []string       `json:"pipelines"`
	Events    []string       `json:"events"`
	Config    map[string]any `json:"config"`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// ListNotifications returns every notification config.
func (d *DB) ListNotifications(ctx context.Context) ([]Notification, error) {
	rows, err := d.QueryContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "mentions", "createdAt", "updatedAt"
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
	row := d.QueryRowContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "mentions", "createdAt", "updatedAt"
FROM "Notification" WHERE id = $1`, id)
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
  "mentions", "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`,
		n.ID, n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		cfg.mentions, now, now,
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
UPDATE "Notification" SET name = $1, enabled = $2, type = $3, pipelines = $4, events = $5,
  "webhookUrl" = $6, "webhookSecret" = $7, "slackUrl" = $8, "slackChannel" = $9, "discordUrl" = $10,
  "mentions" = $11, "updatedAt" = $12
WHERE id = $13`,
		n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		cfg.mentions, prismaNow(), n.ID,
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
	res, err := d.ExecContext(ctx, `DELETE FROM "Notification" WHERE id = $1`, id)
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
	var webhookURL, webhookSecret, slackURL, slackChannel, discordURL, mentions sql.NullString
	var createdAt, updatedAt prismaTime
	if err := s.Scan(
		&n.ID, &n.Name, &n.Enabled, &n.Type, &pipelines, &events,
		&webhookURL, &webhookSecret, &slackURL, &slackChannel, &discordURL,
		&mentions, &createdAt, &updatedAt,
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
		// Rehydrate per-event mention rules. The web editor reads
		// Config.mentions to render each event's picker; without this
		// an explicit "none" (or a custom role) saved earlier would
		// come back empty and revert to the event default on reload.
		if mentions.Valid && mentions.String != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(mentions.String), &m); err == nil && len(m) > 0 {
				n.Config["mentions"] = m
			}
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
	mentions      sql.NullString // JSON map[event]rule, discord only
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
		// Per-event mention rules are an open-ended map, so they don't
		// fit a typed column — JSON-encode the whole thing. Previously
		// dropped entirely, which is why an explicit "none" never
		// persisted and reverted to the @here default for error events.
		// Stored only when non-empty (empty = all defaults; the web
		// layer strips rules that match the event default).
		if m, ok := cfg["mentions"].(map[string]any); ok && len(m) > 0 {
			if b, err := json.Marshal(m); err == nil {
				out.mentions = sql.NullString{String: string(b), Valid: true}
			}
		}
	}
	return out
}

func coalesceStringSlice(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
