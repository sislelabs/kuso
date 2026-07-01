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

// OutboxClaimLease is how far into the future ClaimOutboxRow pushes a
// claimed row's nextAttemptAt while delivery is in flight. This is the
// lock that replaces holding the DB transaction open across the HTTP/
// SMTP call: the claim commits immediately (releasing the connection),
// but the pushed-out nextAttemptAt stops any other worker re-claiming
// the row until the lease expires. It MUST exceed the worst-case
// delivery timeout so a slow-but-succeeding delivery isn't double-sent.
// If a worker crashes mid-delivery the lease expires and another worker
// re-claims — that's the at-least-once guarantee. 2min comfortably
// covers the per-channel HTTP/SMTP timeouts (seconds, not minutes).
const OutboxClaimLease = 2 * time.Minute

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
		VALUES ($1, $2, $3)
		RETURNING "id"
	`, notificationID, eventType, payload).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("enqueue outbox: %w", err)
	}
	return id, nil
}

// ClaimOutboxRow grabs the next due row exclusively and returns it
// with NO transaction held. Pattern: SELECT … FOR UPDATE SKIP LOCKED
// LIMIT 1 inside a SHORT transaction that immediately pushes the row's
// nextAttemptAt OutboxClaimLease into the future, then COMMITS. The
// committed lease — not a held row lock — is what stops other workers
// re-claiming the row while its delivery is in flight.
//
// This is the fix for the connection-pool-exhaustion hazard: the DB
// connection is released the instant the claim commits, so the caller
// performs the (potentially slow / hung) HTTP/SMTP delivery OUTSIDE any
// transaction, then records the outcome in a SECOND short transaction
// via MarkOutboxDelivered / MarkOutboxAttempt. A slow webhook no longer
// pins a DB connection + row lock for its whole timeout.
//
// At-least-once is preserved: if the worker crashes after claiming but
// before marking, the lease expires (OutboxClaimLease later) and another
// worker re-claims. On the success/normal-failure path the caller stamps
// the real deliveredAt or backoff nextAttemptAt, superseding the lease.
//
// Returns ErrNoOutboxRow when the queue is empty / nothing due yet
// (the worker should sleep instead of treating this as an error).
func (d *DB) ClaimOutboxRow(ctx context.Context) (*OutboxRow, error) {
	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("claim outbox: begin: %w", err)
	}
	row := tx.QueryRowContext(ctx, `
		SELECT "id", "notificationId", "eventType", "payload",
		       "attempts", COALESCE("lastError", ''),
		       "nextAttemptAt", "deliveredAt", "createdAt"
		FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NULL
		  AND "nextAttemptAt" <= CURRENT_TIMESTAMP
		  AND "attempts" < $1
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
			return nil, ErrNoOutboxRow
		}
		return nil, fmt.Errorf("claim outbox: scan: %w", err)
	}
	if deliveredAt.Valid {
		t := deliveredAt.Time
		r.DeliveredAt = &t
	}
	// Lease the row: push nextAttemptAt out so no other worker re-claims
	// it while we deliver. Committed immediately — the row lock (and the
	// connection) is released here, not after delivery.
	lease := time.Now().Add(OutboxClaimLease)
	if _, err := tx.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "nextAttemptAt" = $1
		WHERE "id" = $2
	`, lease.UTC().Format("2006-01-02 15:04:05.999999"), r.ID); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("claim outbox: lease: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("claim outbox: commit lease: %w", err)
	}
	return &r, nil
}

// MarkOutboxDelivered stamps deliveredAt=now() on a claimed+delivered
// row in its own short statement — no caller-held transaction. Runs
// AFTER the (out-of-band) delivery succeeded, superseding the claim
// lease ClaimOutboxRow stamped. The row stays in the table (operators
// can audit successful deliveries) until the daily cleanup prunes it.
func (d *DB) MarkOutboxDelivered(ctx context.Context, id int64) error {
	if _, err := d.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "deliveredAt" = CURRENT_TIMESTAMP,
		    "lastError" = NULL
		WHERE "id" = $1
	`, id); err != nil {
		return fmt.Errorf("mark delivered: %w", err)
	}
	return nil
}

// MarkOutboxAttempt records a failed delivery: attempts++ + lastError +
// nextAttemptAt advanced by the caller's backoff. Runs in its own short
// statement after the out-of-band delivery failed, overwriting the claim
// lease with the real backoff schedule so the retry lands when intended.
func (d *DB) MarkOutboxAttempt(ctx context.Context, id int64, errMsg string, nextAttemptAt time.Time) error {
	if _, err := d.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "attempts" = "attempts" + 1,
		    "lastError" = $1,
		    "nextAttemptAt" = $2
		WHERE "id" = $3
	`, errMsg, nextAttemptAt.UTC().Format("2006-01-02 15:04:05.999999"), id); err != nil {
		return fmt.Errorf("mark attempt: %w", err)
	}
	return nil
}

// PruneOutboxDelivered drops successfully-delivered rows older than
// `before`. Called from the daily cleanup tick. We never prune dead-
// letter rows (attempts >= cap, deliveredAt IS NULL) — those are the
// audit trail operators dig through after a webhook outage.
func (d *DB) PruneOutboxDelivered(ctx context.Context, before time.Time) (int64, error) {
	res, err := d.ExecContext(ctx, `
		DELETE FROM "NotificationOutbox"
		WHERE "deliveredAt" IS NOT NULL
		  AND "deliveredAt" < $1
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
		  AND "attempts" < $1
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
		  AND "attempts" >= $1
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
