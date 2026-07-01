package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
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

// notifConfigColumnOnce guards the lazy DDL that adds the generic
// "config" JSON column to the Notification table. The typed per-key
// columns (webhookUrl, slackUrl, discordUrl, …) only cover webhook /
// slack / discord; telegram, pushover, mattermost and email carry
// open-ended config (bot tokens, chat ids, SMTP creds) that has no
// typed home. Without a column to hold it, that config was silently
// dropped on write and came back empty at delivery time, so every
// send dead-lettered. The "config" column stores the full config map
// as JSON for ALL types so the create → store → scan → deliver
// round-trip is lossless. Guarded per-*DB so tests spinning up fresh
// databases each ensure their own copy.
var notifConfigColumnOnce sync.Map // *DB -> *sync.Once

// ensureNotificationConfigColumn adds the "config" JSON column if it's
// missing. Idempotent (ADD COLUMN IF NOT EXISTS) and run at most once
// per *DB via sync.Once so the hot Create/Update/List path doesn't
// re-issue DDL on every call. Kept out of schema.sql deliberately: the
// column is owned by the notify subsystem and this keeps the migration
// co-located with the code that depends on it (mirrors log_partition).
func (d *DB) ensureNotificationConfigColumn(ctx context.Context) error {
	onceAny, _ := notifConfigColumnOnce.LoadOrStore(d, &sync.Once{})
	once := onceAny.(*sync.Once)
	var derr error
	once.Do(func() {
		_, err := d.ExecContext(ctx,
			`ALTER TABLE "Notification" ADD COLUMN IF NOT EXISTS "config" TEXT`)
		if err != nil {
			// Reset so a transient failure (e.g. ctx timeout) can retry
			// on the next call instead of permanently poisoning the once.
			notifConfigColumnOnce.Delete(d)
			derr = fmt.Errorf("db: ensure notification config column: %w", err)
		}
	})
	return derr
}

// ListNotifications returns every notification config.
func (d *DB) ListNotifications(ctx context.Context) ([]Notification, error) {
	if err := d.ensureNotificationConfigColumn(ctx); err != nil {
		return nil, err
	}
	rows, err := d.QueryContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "mentions", "config", "createdAt", "updatedAt"
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
	if err := d.ensureNotificationConfigColumn(ctx); err != nil {
		return nil, err
	}
	row := d.QueryRowContext(ctx, `
SELECT id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "mentions", "config", "createdAt", "updatedAt"
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
	if err := d.ensureNotificationConfigColumn(ctx); err != nil {
		return err
	}
	pj, _ := json.Marshal(coalesceStringSlice(n.Pipelines))
	ej, _ := json.Marshal(coalesceStringSlice(n.Events))
	now := prismaNow()
	cfg := configCols(n.Type, n.Config)
	_, err := d.ExecContext(ctx, `
INSERT INTO "Notification" (id, name, enabled, type, pipelines, events,
  "webhookUrl", "webhookSecret", "slackUrl", "slackChannel", "discordUrl",
  "mentions", "config", "createdAt", "updatedAt")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		n.ID, n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		cfg.mentions, cfg.config, now, now,
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
	if err := d.ensureNotificationConfigColumn(ctx); err != nil {
		return err
	}
	pj, _ := json.Marshal(coalesceStringSlice(n.Pipelines))
	ej, _ := json.Marshal(coalesceStringSlice(n.Events))
	cfg := configCols(n.Type, n.Config)
	res, err := d.ExecContext(ctx, `
UPDATE "Notification" SET name = $1, enabled = $2, type = $3, pipelines = $4, events = $5,
  "webhookUrl" = $6, "webhookSecret" = $7, "slackUrl" = $8, "slackChannel" = $9, "discordUrl" = $10,
  "mentions" = $11, "config" = $12, "updatedAt" = $13
WHERE id = $14`,
		n.Name, n.Enabled, n.Type, string(pj), string(ej),
		cfg.webhookURL, cfg.webhookSecret, cfg.slackURL, cfg.slackChannel, cfg.discordURL,
		cfg.mentions, cfg.config, prismaNow(), n.ID,
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
	var webhookURL, webhookSecret, slackURL, slackChannel, discordURL, mentions, config sql.NullString
	var createdAt, updatedAt prismaTime
	if err := s.Scan(
		&n.ID, &n.Name, &n.Enabled, &n.Type, &pipelines, &events,
		&webhookURL, &webhookSecret, &slackURL, &slackChannel, &discordURL,
		&mentions, &config, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	n.CreatedAt = createdAt.Time
	n.UpdatedAt = updatedAt.Time
	_ = json.Unmarshal([]byte(pipelines), &n.Pipelines)
	_ = json.Unmarshal([]byte(events), &n.Events)
	n.Config = map[string]any{}
	// Base the config on the generic JSON blob first — it's the only
	// home for telegram / pushover / mattermost / email config (bot
	// tokens, chat ids, SMTP creds). The typed columns below overlay
	// it for webhook / slack / discord, which keeps old rows (written
	// before the config column existed, so config IS NULL) working and
	// the discord-mentions round-trip intact.
	if config.Valid && config.String != "" {
		var m map[string]any
		if err := json.Unmarshal([]byte(config.String), &m); err == nil && len(m) > 0 {
			n.Config = m
		}
	}
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
	config        sql.NullString // full config map as JSON, all types
}

func configCols(typ string, cfg map[string]any) notifConfigCols {
	out := notifConfigCols{}
	if cfg == nil {
		return out
	}
	// Persist the full config map as JSON regardless of type. This is
	// what telegram / pushover / mattermost / email delivery reads back
	// (the typed columns below only cover webhook / slack / discord). It
	// also carries any extra keys the typed columns don't model, so the
	// create → store → scan → deliver round-trip is lossless.
	if len(cfg) > 0 {
		if b, err := json.Marshal(cfg); err == nil {
			out.config = sql.NullString{String: string(b), Valid: true}
		}
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
