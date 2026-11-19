// Outbox worker pool — drains NotificationOutbox rows and delivers
// to webhooks with exponential backoff. The dispatcher's Emit path
// enqueues one row per matching channel; this file owns the drain
// side. Workers compete via SELECT … FOR UPDATE SKIP LOCKED so
// multiple in-process workers (and even multiple replicas) drain in
// parallel without double-delivery.
//
// Lifetime: the pool is leader-gated by the dispatcher's existing
// leader hook so multi-replica installs don't N-times-deliver the
// same row. The SKIP LOCKED guard would prevent double-delivery
// across replicas anyway, but skipping the entire workload on
// followers avoids the wasted SELECTs.

package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"runtime/debug"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/safego"
)

// outboxDefaultWorkers is the per-replica worker count. Each worker
// holds at most one DB row + one HTTP request in flight, so 10 is
// comfortable on a single-replica install (10 × 8s = 80s of
// concurrent HTTP-bound capacity). Operators can override via
// KUSO_NOTIFY_OUTBOX_WORKERS in cmd/kuso-server.
const outboxDefaultWorkers = 10

// outboxIdleSleep is the wait between poll attempts when the queue
// is empty. Short enough that a freshly-enqueued event delivers
// within a tick; long enough that an idle install doesn't burn
// SELECT cycles. 1s is the sweet spot — same shape the build
// poller uses.
const outboxIdleSleep = 1 * time.Second

// outboxBackoffBase is the exponential-backoff base for failed
// deliveries: 5s, 10s, 20s, 40s, …, capped at 5min. Across the cap
// at attempts=10 that's ~17 min of recovery before the row goes
// dead-letter — long enough for Slack/Discord throttle windows to
// clear but not so long the queue accumulates a working set.
const outboxBackoffBase = 5 * time.Second

// outboxBackoffMax bounds the per-row sleep so a row that's been
// retrying for hours doesn't end up scheduled days into the future.
// Capped at 5 min — the next attempt is also where a transient
// outage gets noticed by an operator watching kuso_notify_outbox_
// pending stay > 0.
const outboxBackoffMax = 5 * time.Minute

// StartOutboxWorkers launches the configured number of worker
// goroutines. Each runs claim → deliver → mark in a loop until ctx
// is canceled. Safe to call once at boot from the leader-gated
// startSingletons closure; SKIP LOCKED makes additional replicas
// running the same workers correctness-safe but it'd waste their
// DB time. The dispatcher's existing leader hook handles the gate.
//
// workers=0 selects the default. Logger nil-safe via the
// dispatcher's logger.
func (d *Dispatcher) StartOutboxWorkers(ctx context.Context, workers int) {
	if d == nil || d.db == nil {
		return
	}
	if workers <= 0 {
		workers = outboxDefaultWorkers
	}
	for i := 0; i < workers; i++ {
		i := i
		safego.Go(d.logger, "notify-outbox-worker", func() { d.outboxWorker(ctx, i) })
	}
}

// outboxWorker is the per-goroutine drain loop. Claims one row at a
// time (the claim commits immediately with a lease), delivers OUTSIDE
// any transaction, then records the outcome: deliveredAt=now() on
// success, attempts++ + nextAttemptAt advanced on failure. The DB
// connection is never held across the HTTP/SMTP delivery.
func (d *Dispatcher) outboxWorker(ctx context.Context, id int) {
	for {
		if ctx.Err() != nil {
			return
		}
		if !d.shouldRunOutbox() {
			// Not the leader — sleep and re-check. The dispatcher's
			// existing fast-path leader gate keeps webhook delivery
			// pinned to one replica.
			if !sleepWithCtx(ctx, outboxIdleSleep) {
				return
			}
			continue
		}
		processed, err := d.drainOne(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Warn("notify: outbox drain", "worker", id, "err", err)
			// Brief sleep so we don't tight-loop on a DB error.
			if !sleepWithCtx(ctx, outboxIdleSleep) {
				return
			}
			continue
		}
		if !processed {
			// Empty queue — sleep before re-polling.
			if !sleepWithCtx(ctx, outboxIdleSleep) {
				return
			}
		}
	}
}

// drainOne claims + processes exactly one outbox row. Returns
// (true, nil) when a row was claimed (success or scheduled retry);
// (false, nil) when the queue was empty; (false, err) on a real DB
// or transaction error.
//
// A panic while processing the row is recovered here and recorded as a
// failed attempt. Left to unwind, it would end the worker goroutine and
// leave the row's attempts untouched, so the next worker re-claims it
// after the lease and dies the same way until none are left.
func (d *Dispatcher) drainOne(ctx context.Context) (processed bool, err error) {
	row, err := d.db.ClaimOutboxRow(ctx)
	if err != nil {
		if errors.Is(err, db.ErrNoOutboxRow) {
			return false, nil
		}
		return false, err
	}
	defer func() {
		r := recover()
		if r == nil {
			return
		}
		d.logger.Error("notify: outbox row panicked during delivery",
			"row", row.ID, "channelId", row.NotificationID, "attempt", row.Attempts+1,
			"panic", r, "stack", string(debug.Stack()))
		processed = true
		err = d.recordFailedAttempt(ctx, row, fmt.Sprintf("panic: %v", r), 0)
	}()
	// The claim already committed (with a lease pushing nextAttemptAt
	// into the future), so NO DB connection is held past this point.
	// Delivery happens outside any transaction; the outcome is recorded
	// with a fresh short statement (Mark*), superseding the lease.
	//
	// Unmarshal the event payload first so a corrupt row goes straight
	// to dead-letter instead of being delivered as garbage.
	var ev Event
	if uerr := json.Unmarshal(row.Payload, &ev); uerr != nil {
		_ = d.db.MarkOutboxAttempt(ctx, row.ID,
			"payload unmarshal: "+uerr.Error(),
			time.Now().Add(outboxBackoffMax),
		)
		return true, nil
	}
	// Fetch the channel config (URL, secret, mention rules).
	lookup := d.lookupChannelFn
	if lookup == nil {
		lookup = d.lookupNotification
	}
	notif, nerr := lookup(ctx, row.NotificationID)
	if errors.Is(nerr, errChannelNotFound) {
		// Channel deleted between Emit and worker-drain. Terminal — the
		// user removed the channel. Marking as delivered drops the row
		// from the pending count without spamming dead-letter.
		_ = d.db.MarkOutboxDelivered(ctx, row.ID)
		d.logger.Info("notify: outbox row's channel no longer exists, skipping", "row", row.ID, "channelId", row.NotificationID)
		return true, nil
	}
	if nerr != nil {
		// Transient (DB blip, ctx timeout). Leave the row untouched: the
		// claim lease expires and a worker retries it, without burning
		// an attempt on a failure that says nothing about the channel.
		return true, fmt.Errorf("lookup channel %s: %w", row.NotificationID, nerr)
	}
	if !notif.Enabled {
		// Channel disabled — same treatment as deleted. User can re-
		// enable later, but past-pending deliveries aren't replayed.
		_ = d.db.MarkOutboxDelivered(ctx, row.ID)
		return true, nil
	}
	derr := d.deliverViaChannel(ctx, notif, ev)
	if derr == nil {
		if merr := d.db.MarkOutboxDelivered(ctx, row.ID); merr != nil {
			return true, merr
		}
		metricsDelivered.WithLabelValues(string(ev.Type)).Inc()
		return true, nil
	}
	var ue *upstreamError
	if errors.As(derr, &ue) {
		if ue.permanent() {
			if merr := d.deadLetterOutboxRow(ctx, row.ID, derr.Error()); merr != nil {
				return true, merr
			}
			d.logger.Error("notify: channel rejected delivery permanently — dead-letter",
				"row", row.ID, "channelId", notif.ID, "type", string(ev.Type),
				"lastError", derr.Error())
			return true, nil
		}
		return true, d.recordFailedAttempt(ctx, row, derr.Error(), ue.RetryAfter)
	}
	return true, d.recordFailedAttempt(ctx, row, derr.Error(), 0)
}

// recordFailedAttempt advances the row's retry schedule: the receiver's
// Retry-After when it sent one, else backoff = base * 2^attempts with
// ±20% jitter so synchronised retries don't reconverge on one instant.
func (d *Dispatcher) recordFailedAttempt(ctx context.Context, row *db.OutboxRow, reason string, retryAfter time.Duration) error {
	nextAttempts := row.Attempts + 1
	wait := outboxBackoff(nextAttempts)
	if retryAfter > 0 {
		wait = min(retryAfter, outboxBackoffMax)
	}
	if merr := d.db.MarkOutboxAttempt(ctx, row.ID, reason, time.Now().Add(wait)); merr != nil {
		return merr
	}
	if nextAttempts >= db.MaxOutboxAttempts {
		// Crossed the cap with this attempt → permanent dead-letter.
		// Log loud enough that operators see it in `kubectl logs`
		// without grepping; metrics also expose the count.
		d.logger.Error("notify: outbox row exhausted retries — dead-letter",
			"row", row.ID, "channelId", row.NotificationID, "type", row.EventType,
			"lastError", reason)
	}
	return nil
}

// deadLetterOutboxRow moves a row straight to dead-letter (attempts at
// the cap, which ClaimOutboxRow and the pending count both exclude).
func (d *Dispatcher) deadLetterOutboxRow(ctx context.Context, id int64, reason string) error {
	if _, err := d.db.ExecContext(ctx, `
		UPDATE "NotificationOutbox"
		SET "attempts" = GREATEST("attempts" + 1, $1),
		    "lastError" = $2
		WHERE "id" = $3
	`, db.MaxOutboxAttempts, reason, id); err != nil {
		return fmt.Errorf("dead-letter outbox row: %w", err)
	}
	return nil
}

// outboxBackoff returns the sleep window for the Nth attempt
// (1-indexed). Exponential growth × 2 with jitter, capped at
// outboxBackoffMax. Jitter is ±20% deterministic-randomly seeded
// per-call so simultaneous failures don't reconverge.
func outboxBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := outboxBackoffBase << (attempt - 1)
	if base <= 0 || base > outboxBackoffMax {
		base = outboxBackoffMax
	}
	jitter := time.Duration(rand.Int64N(int64(base) / 5))
	if rand.IntN(2) == 0 {
		return base - jitter
	}
	return base + jitter
}

// deliverViaChannel sends a single event to a single configured
// channel. Mirrors the type switch the in-process dispatch used to
// do, but goes through the sync helpers (sendDiscordSync /
// sendWebhookSync) so we can capture the per-attempt error for
// nextAttempt's lastError.
func (d *Dispatcher) deliverViaChannel(ctx context.Context, n db.Notification, e Event) error {
	switch n.Type {
	case "discord":
		url, _ := n.Config["url"].(string)
		if url == "" {
			return errors.New("channel has no webhook URL")
		}
		return d.sendDiscordSync(ctx, url, e, mentionFor(e, n.Config))
	case "webhook":
		url, _ := n.Config["url"].(string)
		if url == "" {
			return errors.New("channel has no webhook URL")
		}
		secret, _ := n.Config["secret"].(string)
		return d.sendWebhookSync(ctx, url, secret, e)
	case "slack", "mattermost":
		// Both consume Slack's incoming-webhook JSON shape.
		url, _ := n.Config["url"].(string)
		if url == "" {
			return errors.New("channel has no webhook URL")
		}
		return d.sendSlackSync(ctx, url, e)
	case "telegram":
		token, _ := n.Config["botToken"].(string)
		chatID, _ := n.Config["chatId"].(string)
		return d.sendTelegramSync(ctx, token, chatID, e)
	case "pushover":
		token, _ := n.Config["token"].(string)
		user, _ := n.Config["user"].(string)
		return d.sendPushoverSync(ctx, token, user, e)
	case "email":
		return d.sendEmailSync(ctx, n.Config, e)
	default:
		return errors.New("unknown channel type: " + n.Type)
	}
}

// deliverableChannel reports whether a channel type has a working
// delivery path (i.e. deliverViaChannel handles it). The enqueue
// whitelist uses this so a channel kind with no sender never piles up
// undeliverable rows in the outbox.
func deliverableChannel(t string) bool {
	switch t {
	case "discord", "webhook", "slack", "mattermost", "telegram", "pushover", "email":
		return true
	default:
		return false
	}
}

// errChannelNotFound means the channel row genuinely doesn't exist
// (confirmed by the DB, not just absent from the cache).
var errChannelNotFound = errors.New("channel not found")

// lookupNotification reads a single channel by id, checking the
// in-process cache first. A cache miss falls through to a direct DB
// read: the cache can be up to notifsCacheTTL stale, and on multi-
// replica installs a channel created on another replica is invisible
// to this one's cache until it expires. Only a DB "not found" yields
// errChannelNotFound; any other failure is returned as-is (transient).
func (d *Dispatcher) lookupNotification(ctx context.Context, id string) (db.Notification, error) {
	notifs, err := d.cachedNotifications(ctx)
	if err != nil {
		return db.Notification{}, err
	}
	for _, n := range notifs {
		if n.ID == id {
			return n, nil
		}
	}
	n, err := d.db.FindNotification(ctx, id)
	if errors.Is(err, db.ErrNotFound) {
		return db.Notification{}, errChannelNotFound
	}
	if err != nil {
		return db.Notification{}, err
	}
	return *n, nil
}

// shouldRunOutbox folds the leader gate into one predicate. nil
// leaderFn means "always on" (single-replica installs, tests).
func (d *Dispatcher) shouldRunOutbox() bool {
	d.mu.Lock()
	leaderFn := d.isLeader
	d.mu.Unlock()
	if leaderFn == nil {
		return true
	}
	return leaderFn()
}

// sleepWithCtx returns false when ctx was canceled during the wait.
// Inline helper so the worker loop doesn't sprout a time.NewTimer
// dance at every poll.
func sleepWithCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
