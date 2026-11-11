package github

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/builds"
)

// keyedSerializer runs submitted funcs one at a time per key, in
// submission order. Different keys run concurrently. A key with nothing
// queued holds no goroutine and no map entry.
type keyedSerializer struct {
	mu sync.Mutex
	// queues[key] exists while a worker goroutine drains that key; the
	// slice holds the funcs still waiting behind the running one.
	queues map[string][]func()
}

func (s *keyedSerializer) submit(key string, fn func()) {
	s.mu.Lock()
	if q, running := s.queues[key]; running {
		s.queues[key] = append(q, fn)
		s.mu.Unlock()
		return
	}
	if s.queues == nil {
		s.queues = map[string][]func(){}
	}
	s.queues[key] = nil
	s.mu.Unlock()
	go func() {
		next := fn
		for {
			next()
			s.mu.Lock()
			q := s.queues[key]
			if len(q) == 0 {
				delete(s.queues, key)
				s.mu.Unlock()
				return
			}
			next, s.queues[key] = q[0], q[1:]
			s.mu.Unlock()
		}
	}()
}

// dispatchKey returns the ordering key for a webhook delivery: one PR of
// one repo, or one ref of one repo. Deliveries that share a key mutate the
// same preview env, clone DB and builds, so they must not interleave
// (a `closed` finishing before a slower `synchronize` let the latter
// recreate everything the close just tore down). "" = no ordering needed.
func dispatchKey(event string, body []byte) string {
	var p struct {
		Number     int    `json:"number"`
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(body, &p) != nil {
		return ""
	}
	repo := strings.ToLower(p.Repository.FullName)
	switch event {
	case "pull_request":
		if p.Number == 0 {
			return ""
		}
		return "pr:" + repo + "#" + strconv.Itoa(p.Number)
	case "push":
		return "push:" + repo + "@" + p.Ref
	}
	return ""
}

// DispatchAsync runs Dispatch off the request goroutine. Deliveries for the
// same PR (or the same pushed ref) run one after another in arrival order;
// each gets its own timeout, started when it begins running rather than
// while it waits. Ordering is per process: a second server replica
// receiving the same PR's deliveries is not coordinated with this one.
//
// onSuccess (may be nil) runs only after Dispatch returns nil; the webhook
// handler records the delivery for dedup there, so a delivery that failed
// or never ran (server restart with a queue) can still be redelivered.
func (d *Dispatcher) DispatchAsync(parent context.Context, event, deliveryID string, body []byte, timeout time.Duration, onSuccess func()) {
	run := func() {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if err := d.Dispatch(ctx, event, body); err != nil {
			d.Logger.Error("github dispatch", "event", event, "delivery", deliveryID, "err", err)
			d.notifyDispatchFailed(event, deliveryID, err)
			return
		}
		if onSuccess != nil {
			onSuccess()
		}
	}
	key := dispatchKey(event, body)
	if key == "" {
		go run()
		return
	}
	d.serial.submit(key, run)
}

// notifyDispatchFailed reports a delivery whose dispatch failed. GitHub
// already got a 204, so without this the lost deploy is visible only in
// the server log.
func (d *Dispatcher) notifyDispatchFailed(event, deliveryID string, err error) {
	if d.Notifier == nil {
		return
	}
	d.Notifier.Emit(builds.EventEnvelope{
		Type:  "build.failed",
		Title: "GitHub webhook delivery failed",
		Body: fmt.Sprintf("kuso could not finish handling %s delivery %s: %v. "+
			"Redeliver it from the GitHub App's Advanced → Recent Deliveries page, or push again.", event, deliveryID, err),
		Severity: "error",
	})
}

// droppedNotifyEvery caps dropped-delivery notifications per installation:
// a `git push --all` burst drops many deliveries at once, and one card
// saying so is enough.
const droppedNotifyEvery = time.Minute

// NotifyDroppedDelivery reports a webhook delivery the server refused
// (rate limit). GitHub never redelivers on its own, so a dropped push is a
// deploy that silently never happens unless someone is told.
func (d *Dispatcher) NotifyDroppedDelivery(installationID int64, event, deliveryID string) {
	if d.Notifier == nil {
		return
	}
	d.closedMu.Lock()
	if d.droppedNotifiedAt == nil {
		d.droppedNotifiedAt = map[int64]time.Time{}
	}
	if last, ok := d.droppedNotifiedAt[installationID]; ok && time.Since(last) < droppedNotifyEvery {
		d.closedMu.Unlock()
		return
	}
	d.droppedNotifiedAt[installationID] = time.Now()
	d.closedMu.Unlock()
	d.Notifier.Emit(builds.EventEnvelope{
		Type:  "build.failed",
		Title: "GitHub webhook deliveries dropped (rate limit)",
		Body: fmt.Sprintf("kuso refused %s delivery %s from installation %d because the per-installation rate limit was hit. "+
			"GitHub does not retry these: redeliver them from the GitHub App's Advanced → Recent Deliveries page, "+
			"or push again, or the builds they would have started never run.", event, deliveryID, installationID),
		Severity: "error",
	})
}

// closedPRTombstoneTTL bounds how long the in-process fallback remembers
// a close (no DB: tests, DB-less installs). The DB copy is pruned after a
// week by db.MarkGithubPRClosed.
const closedPRTombstoneTTL = 24 * time.Hour

func prKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

// markPRClosed records that a PR was closed at closedAt, so a synchronize
// or opened that was sent before the close and reaches us afterwards
// doesn't rebuild the preview. The tombstone lives in Postgres when the
// dispatcher has one, so every replica sees it and a restart keeps it; the
// in-process copy is the fallback when the DB is absent or fails.
func (d *Dispatcher) markPRClosed(ctx context.Context, repo string, number int, closedAt time.Time) {
	d.closedMu.Lock()
	if d.closedPRs == nil {
		d.closedPRs = map[string]time.Time{}
	}
	for k, at := range d.closedPRs {
		if time.Since(at) > closedPRTombstoneTTL {
			delete(d.closedPRs, k)
		}
	}
	d.closedPRs[prKey(repo, number)] = closedAt
	d.closedMu.Unlock()
	if d.DB != nil {
		if err := d.DB.MarkGithubPRClosed(ctx, repo, number, closedAt); err != nil {
			d.Logger.Warn("record PR close", "repo", repo, "pr", number, "err", err)
		}
	}
}

// clearPRClosed drops the tombstone: the PR was reopened, or opened anew.
func (d *Dispatcher) clearPRClosed(ctx context.Context, repo string, number int) {
	d.closedMu.Lock()
	delete(d.closedPRs, prKey(repo, number))
	d.closedMu.Unlock()
	if d.DB != nil {
		if err := d.DB.ClearGithubPRClosed(ctx, repo, number); err != nil {
			d.Logger.Warn("clear PR close", "repo", repo, "pr", number, "err", err)
		}
	}
}

func (d *Dispatcher) prClosedAt(ctx context.Context, repo string, number int) (time.Time, bool) {
	if d.DB != nil {
		at, ok, err := d.DB.GithubPRClosedAt(ctx, repo, number)
		if err == nil {
			return at, ok
		}
		d.Logger.Warn("read PR close", "repo", repo, "pr", number, "err", err)
	}
	d.closedMu.Lock()
	defer d.closedMu.Unlock()
	at, ok := d.closedPRs[prKey(repo, number)]
	return at, ok && time.Since(at) <= closedPRTombstoneTTL
}

// sentBeforeClose reports whether an opened/synchronize delivery predates
// the PR's recorded close. Both times come from GitHub's payloads, so they
// share a clock; GitHub stamps whole seconds, and a tie counts as before.
// A delivery without timestamps (hand-built ones) can't be ordered: a
// synchronize is assumed late, an opened is a new PR.
func (d *Dispatcher) sentBeforeClose(ctx context.Context, repo string, number int, action string, sentAt time.Time) bool {
	closedAt, ok := d.prClosedAt(ctx, repo, number)
	if !ok {
		return false
	}
	if !sentAt.IsZero() {
		return !sentAt.After(closedAt)
	}
	return action == "synchronize"
}
