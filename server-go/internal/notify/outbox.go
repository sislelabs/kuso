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
	"math/rand/v2"
	"time"

	"kuso/server/internal/db"
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
		go d.outboxWorker(ctx, i)
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
func (d *Dispatcher) drainOne(ctx context.Context) (bool, error) {
	row, err := d.db.ClaimOutboxRow(ctx)
	if err != nil {
		if errors.Is(err, db.ErrNoOutboxRow) {
			return false, nil
		}
		return false, err
	}
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
	// Fetch the channel config (URL, secret, mention rules). The
	// dispatcher already caches Notifications in-memory; on a cache
	// miss this falls through to a direct DB read.
	notif, nerr := d.lookupNotification(ctx, row.NotificationID)
	if nerr != nil {
		// Channel deleted between Emit and worker-drain. Treat as
		// terminal — the user removed the channel; we shouldn't keep
		// retrying. Marking as delivered drops the row from the
		// pending count without spamming dead-letter.
		_ = d.db.MarkOutboxDelivered(ctx, row.ID)
		d.logger.Info("notify: outbox row's channel no longer exists, skipping", "row", row.ID, "channelId", row.NotificationID)
		return true, nil
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
		metricsDispatched.WithLabelValues(string(ev.Type)).Inc()
		return true, nil
	}
	// Failed — advance the schedule. backoff = base * 2^attempts,
	// capped at the max. Add ±20% jitter so synchronised retries
	// across many rows don't reconverge on the same instant.
	nextAttempts := row.Attempts + 1
	wait := outboxBackoff(nextAttempts)
	nextAttemptAt := time.Now().Add(wait)
	if merr := d.db.MarkOutboxAttempt(ctx, row.ID, derr.Error(), nextAttemptAt); merr != nil {
		return true, merr
	}
	if nextAttempts >= db.MaxOutboxAttempts {
		// Crossed the cap with this attempt → permanent dead-letter.
		// Log loud enough that operators see it in `kubectl logs`
		// without grepping; metrics also expose the count.
		d.logger.Error("notify: outbox row exhausted retries — dead-letter",
			"row", row.ID, "channelId", notif.ID, "type", string(ev.Type),
			"lastError", derr.Error())
	}
	return true, nil
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

// lookupNotification reads a single channel by id, checking the
// in-process cache first. The cache already holds the full list
// from cachedNotifications; a linear scan on a ~tens-of-rows list
// is fine and avoids a DB round-trip on the hot path.
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
	return db.Notification{}, errors.New("channel not found")
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
