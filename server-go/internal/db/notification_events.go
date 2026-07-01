// In-app notification feed. Every event the notify dispatcher fires
// is mirrored into this table so the navbar bell can render the
// recent N entries — independent of whether the operator wired up a
// webhook / Discord / Slack sink. Retention is rows-not-time: prune
// the oldest beyond a cap so a noisy cluster doesn't unboundedly
// grow the table.

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
	// Classification is the wire shape from internal/failures, kept
	// here as a json.RawMessage so the db package doesn't depend on
	// failures (which would invert the layering — db is below domain
	// code). The notify dispatcher serialises the typed Classification
	// before insert; the bell-icon handler passes the raw JSON to the
	// browser, where the TypeScript types decode it. Nil when the
	// event isn't a classified failure.
	Classification json.RawMessage `json:"classification,omitempty"`
}

const notificationEventCap = 200

// PruneNotificationEvents deletes events whose createdAt is older
// than `before`. Returns the number of rows removed. Called from
// the daily cleanup goroutine in cmd/kuso-server. Independent of
// the per-insert row-cap prune above — that one keeps the bell
// icon snappy; this one keeps the table from accumulating dead
// rows on a long-running cluster with low event volume.
func (d *DB) PruneNotificationEvents(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx,
		`DELETE FROM "NotificationEvent" WHERE "createdAt" < $1`,
		before.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return 0, fmt.Errorf("prune notification events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

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
	// Classification rides through as a JSONB column when present.
	// Stored as the typed wire shape (kind/tab/summary/lineHint/lineNum)
	// so the browser handler doesn't need to re-derive it on every
	// list call.
	var classification any // any → driver picks string OR nil for NULL
	if len(e.Classification) > 0 {
		classification = string(e.Classification)
	}
	// INSERT + cap-prune as ONE statement via a CTE. This replaces the
	// former explicit 2-statement BEGIN/…/COMMIT transaction, which
	// serialised every emitting goroutine on the DB (two round-trips +
	// a held connection per event). Emit is called from build/health/
	// node watchers, and under a build storm (50+ events/sec during a
	// deploy burst) that per-event txn became the bottleneck.
	//
	// A single CTE is atomic on its own — no explicit transaction
	// needed — and closes the interleave race the transaction guarded
	// against: the INSERT and the prune's subquery run under one
	// statement snapshot, so two concurrent Emits can no longer sequence
	// as insert(G1) → prune(G2 over a snapshot missing G1's row) and
	// delete a row the user was supposed to see. The data-modifying CTE
	// snapshot means the prune's subquery does NOT observe this call's
	// own just-inserted row, so steady-state the table sits at cap+1
	// rather than exactly cap — within the documented "never exceeds the
	// cap by more than the in-flight insert count" tolerance, and it
	// never grows unbounded.
	//
	// The prune deletes everything below the cap-th id from the top.
	// The id is monotonic so the "newest N" set is just the highest N
	// ids; the OFFSET form lets the planner stop after scanning N rows
	// of the descending PK b-tree (vs the old O(n log n) NOT IN, which
	// Postgres had to materialise and which locked the table during
	// build storms).
	//
	// A prune failure can't partially apply here (single statement), so
	// there's no "commit the INSERT but skip the prune" fallback to make
	// — either both land or neither does, and a hard failure surfaces as
	// the error below (Emit logs + proceeds; the feed row is the load-
	// bearing part and a retry lands it).
	if _, err := d.ExecContext(ctx, `
		WITH inserted AS (
			INSERT INTO "NotificationEvent" ("type","title","body","severity","project","service","url","extra","classification")
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		)
		DELETE FROM "NotificationEvent"
		WHERE "id" < (
			SELECT "id" FROM "NotificationEvent"
			ORDER BY "id" DESC
			LIMIT 1 OFFSET $10
		)`,
		e.Type, e.Title, e.Body, e.Severity, e.Project, e.Service, e.URL, extraJSON, classification,
		notificationEventCap,
	); err != nil {
		return fmt.Errorf("insert notification event: %w", err)
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
	q := `SELECT "id","type","title","body","severity","project","service","url","extra","createdAt","readAt","classification"
	      FROM "NotificationEvent"`
	if unreadOnly {
		q += ` WHERE "readAt" IS NULL`
	}
	q += ` ORDER BY "id" DESC LIMIT $1`
	rows, err := d.QueryContext(ctx, q, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification events: %w", err)
	}
	defer rows.Close()
	out := []NotificationEvent{}
	for rows.Next() {
		var e NotificationEvent
		var body, project, service, url, extra, classification sql.NullString
		var created, read sql.NullTime
		if err := rows.Scan(&e.ID, &e.Type, &e.Title, &body, &e.Severity,
			&project, &service, &url, &extra, &created, &read, &classification); err != nil {
			return nil, fmt.Errorf("scan notification event: %w", err)
		}
		e.Body = body.String
		e.Project = project.String
		e.Service = service.String
		e.URL = url.String
		if extra.Valid && extra.String != "" {
			_ = json.Unmarshal([]byte(extra.String), &e.Extra)
		}
		if classification.Valid && classification.String != "" {
			e.Classification = json.RawMessage(classification.String)
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

// ListNotificationEventsForProjects returns the newest `limit` events
// whose project is in the given allowlist. Non-admin callers see
// only events scoped to projects they're a member of; admins should
// use ListNotificationEvents to see everything. Empty projects =>
// empty result.
//
// readAt is dropped from the projection because the per-user read
// model doesn't exist yet (the column is a single global flag); a
// non-admin feed is fire-and-forget so we don't show stale read
// state from another user's clicks.
func (d *DB) ListNotificationEventsForProjects(ctx context.Context, limit int, projects []string) ([]NotificationEvent, error) {
	if limit <= 0 || limit > notificationEventCap {
		limit = notificationEventCap
	}
	if len(projects) == 0 {
		return []NotificationEvent{}, nil
	}
	// Cap the IN-clause size. Postgres's max_function_args is
	// 32,767 — far above realistic project memberships in a single-
	// tenant install (tens at most). But an attacker who somehow
	// inflates the tenancy table could trigger a degraded query
	// plan; truncate to the first 500 projects (alphabetised, so
	// the cut is deterministic). B8 in followup review.
	const maxIN = 500
	if len(projects) > maxIN {
		projects = projects[:maxIN]
	}
	// Build a $N placeholder list. We use sql.NamedArg-style numbered
	// placeholders inline because the prismaTime/lib-pq driver
	// rewriter doesn't expand IN ? with a slice for us.
	placeholders := make([]string, len(projects))
	args := make([]any, 0, len(projects)+1)
	for i, p := range projects {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args = append(args, p)
	}
	args = append(args, limit)
	q := fmt.Sprintf(`
		SELECT "id","type","title","body","severity","project","service","url","extra","createdAt","readAt","classification"
		FROM "NotificationEvent"
		WHERE "project" IN (%s)
		ORDER BY "id" DESC
		LIMIT $%d
	`, joinComma(placeholders), len(projects)+1)
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list scoped notification events: %w", err)
	}
	defer rows.Close()
	out := []NotificationEvent{}
	for rows.Next() {
		var e NotificationEvent
		var body, project, service, url, extra, classification sql.NullString
		var created, read sql.NullTime
		if err := rows.Scan(&e.ID, &e.Type, &e.Title, &body, &e.Severity,
			&project, &service, &url, &extra, &created, &read, &classification); err != nil {
			return nil, fmt.Errorf("scan notification event: %w", err)
		}
		e.Body = body.String
		e.Project = project.String
		e.Service = service.String
		e.URL = url.String
		if extra.Valid && extra.String != "" {
			_ = json.Unmarshal([]byte(extra.String), &e.Extra)
		}
		if classification.Valid && classification.String != "" {
			e.Classification = json.RawMessage(classification.String)
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
	row := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "NotificationEvent" WHERE "readAt" IS NULL`)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count unread: %w", err)
	}
	return n, nil
}

// MarkAllNotificationEventsRead stamps readAt=now() on every unread
// row. Called when the user opens the bell popover.
func (d *DB) MarkAllNotificationEventsRead(ctx context.Context) error {
	_, err := d.ExecContext(ctx, `UPDATE "NotificationEvent" SET "readAt" = CURRENT_TIMESTAMP WHERE "readAt" IS NULL`)
	if err != nil {
		return fmt.Errorf("mark all read: %w", err)
	}
	return nil
}

// ClearAllNotificationEvents wipes the entire feed. Called from the
// "Clear" button in the bell popover. The next event the dispatcher
// emits will land cleanly into an empty table — no race with the
// per-insert prune since that prune only looks at id-cap, not by
// timestamp.
func (d *DB) ClearAllNotificationEvents(ctx context.Context) (int64, error) {
	res, err := d.ExecContext(ctx, `DELETE FROM "NotificationEvent"`)
	if err != nil {
		return 0, fmt.Errorf("clear all notification events: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
