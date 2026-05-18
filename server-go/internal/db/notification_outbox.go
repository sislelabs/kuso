// Notification outbox — durable queue for webhook fan-out.
//
// The in-memory dispatcher channel was best-effort: a flaky Slack URL
// would drop events with a warn log + a kuso_notify_dropped_total
// counter increment. That's fine for the bell-icon feed (which goes
// through NotificationEvent directly), but for users who wire up a
// webhook + expect "every deploy lands in our channel" it's not.
//
// The outbox flips webhook delivery from at-most-once to at-least-
// once: Emit enqueues one row per matching channel; an N-worker pool
// drains with exponential backoff; rows past the retry cap stay as
// a dead-letter trail visible via SELECT * FROM "NotificationOutbox"
// WHERE deliveredAt IS NULL AND attempts >= 10.

package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OutboxRow is one outstanding delivery attempt. Workers SELECT one
// row at a time using FOR UPDATE SKIP LOCKED so multiple workers
// (in-process AND across replicas) drain in parallel without
// stepping on each other.
type OutboxRow struct {
	ID             int64
	NotificationID string // FK to Notification.id (kuso uses cuid-shaped strings, not int)
	EventType      string
	Payload        []byte // raw JSON; the worker unmarshals into a notify.Event
	Attempts       int
	LastError      string
	NextAttemptAt  time.Time
	DeliveredAt    *time.Time
	CreatedAt      time.Time
}

// MaxOutboxAttempts caps retries before a row becomes dead-letter.
// 10 attempts with exponential backoff covers ~17 minutes of
// recovery time — enough for transient incidents (Slack throttling,
// brief DNS hiccups) without permanently spinning on a wedged URL.
const MaxOutboxAttempts = 10

// ErrNoOutboxRow signals the worker that the table has no due rows
// right now — sleep and try again, not an error to log.
var ErrNoOutboxRow = errors.New("notification outbox: no due row")

// EnqueueOutbox appends one delivery row per channel that matches an
// emitted event. The dispatcher computes the channel set (whitelist
// + leader-gate) and hands us the JSON-serialised event. Returns the
// row id of the first inserted row for tracing; subsequent rows are
// inserted in the same transaction.
func (d *DB) EnqueueOutbox(ctx context.Context, notificationID string, eventType string, payload []byte) (int64, error) {
	var id int64
	err := d.QueryRowContext(ctx, `
		INSERT INTO "NotificationOutbox"
			("notificationId", "eventType", "payload")
		VALUES (?, ?, ?)
		RETURNING "id"
	`, notificationID, eventType, payload).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enqueue outbox: %w", err)
	}
	return id, nil
}

// ClaimOutboxRow grabs the next due row exclusively. Pattern:
// SELECT ... FOR UPDATE SKIP LOCKED LIMIT 1 inside a transaction —
// the row stays locked until the worker commits the result.
// Workers process the row inside the same transaction so a crash
// mid-delivery rolls back the claim and the next worker retries.
//
// Returns ErrNoOutboxRow when the queue is empty / nothing due yet
// (the worker should sleep instead of treating this as an error).
//
// The returned Tx MUST be committed (with MarkOutboxDelivered or
// MarkOutboxAttempt) or rolled back by the caller.
func (d *DB) ClaimOutboxRow(ctx context.Context) (*sql.Tx, *OutboxRow, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("claim outbox: begin: %w", err)
	}
	row := tx.QueryRowContext(ctx, `
		SELECT "id", "notificationId", "eventType", "payload",
		       "attempts", COALESCE("lastError", ''),
		       "nextAttemptAt", "deliveredAt", "createdAt"
		FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NULL
		  AND "nextAttemptAt" <= CURRENT_TIMESTAMP
		  AND "attempts" < ?
		ORDER BY "nextAttemptAt" ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`, MaxOutboxAttempts)
	var r OutboxRow
	var deliveredAt sql.NullTime
	if err := row.Scan(&r.ID, &r.NotificationID, &r.EventType, &r.Payload,
		&r.Attempts, &r.LastError, &r.NextAttemptAt, &deliveredAt, &r.CreatedAt); err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, ErrNoOutboxRow
		}
		return nil, nil, fmt.Errorf("claim outbox: scan: %w", err)
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time
		r.DeliveredAt = &t
	}
	return tx.Tx, &r, nil
}

// MarkOutboxDelivered commits the claim with deliveredAt=now(). After
// commit the row stays in the table (operators can audit successful
// deliveries) until the daily cleanup goroutine prunes it.
func (d *DB) MarkOutboxDelivered(ctx context.Context, tx *sql.Tx, id int64) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "deliveredAt" = CURRENT_TIMESTAMP,
		    "lastError" = NULL
		WHERE "id" = ?
	`, id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark delivered: %w", err)
	}
	return tx.Commit()
}

// MarkOutboxAttempt commits the claim with attempts++ + lastError +
// nextAttemptAt advanced by backoff. The caller computes the
// backoff window so a future policy change (e.g. capped at 30 min)
// touches one site, not this storage call.
func (d *DB) MarkOutboxAttempt(ctx context.Context, tx *sql.Tx, id int64, errMsg string, nextAttemptAt time.Time) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "attempts" = "attempts" + 1,
		    "lastError" = ?,
		    "nextAttemptAt" = ?
		WHERE "id" = ?
	`, errMsg, nextAttemptAt.UTC().Format("2006-01-02 15:04:05.999999"), id); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("mark attempt: %w", err)
	}
	return tx.Commit()
}

// PruneOutboxDelivered drops successfully-delivered rows older than
// `before`. Called from the daily cleanup tick. We never prune dead-
// letter rows (attempts >= cap, deliveredAt IS NULL) — those are the
// audit trail operators dig through after a webhook outage.
func (d *DB) PruneOutboxDelivered(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `
		DELETE FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NOT NULL
		  AND "deliveredAt" < ?
	`, before.UTC().Format("2006-01-02 15:04:05"))
	if err != nil {
		return 0, fmt.Errorf("prune outbox: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// CountOutboxPending returns the number of rows awaiting delivery
// (deliveredAt IS NULL, attempts < cap). Used by the metrics
// exporter to surface kuso_notify_outbox_pending as a gauge — alert
// when this stays above zero for minutes, indicating a stuck
// webhook.
func (d *DB) CountOutboxPending(ctx context.Context) (int, error) {
	row := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NULL
		  AND "attempts" < ?
	`, MaxOutboxAttempts)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count outbox pending: %w", err)
	}
	return n, nil
}

// CountOutboxDead returns the number of rows past the retry cap.
// Operators alert on this; a non-zero value means at least one
// channel is permanently misconfigured (bad URL, revoked token).
func (d *DB) CountOutboxDead(ctx context.Context) (int, error) {
	row := d.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NULL
		  AND "attempts" >= ?
	`, MaxOutboxAttempts)
	var n int
	if err := row.Scan(&n); err != nil {
		return 0, fmt.Errorf("count outbox dead: %w", err)
	}
	return n, nil
}

// MarshalOutboxPayload serialises the dispatcher's event shape for
// storage. Lives in the db package so storage callers don't have to
// import notify (notify already imports db, so the dep direction
// stays one-way). The dispatcher's enqueue path uses encoding/json
// with the same field names notify.Event publishes.
func MarshalOutboxPayload(v any) ([]byte, error) {
	return json.Marshal(v)
}
