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
func (d *Dispatcher) DispatchAsync(parent context.Context, event string, body []byte, timeout time.Duration) {
	run := func() {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if err := d.Dispatch(ctx, event, body); err != nil {
			d.Logger.Error("github dispatch", "event", event, "err", err)
		}
	}
	key := dispatchKey(event, body)
	if key == "" {
		go run()
		return
	}
	d.serial.submit(key, run)
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

// closedPRTombstoneTTL bounds how long a PR close blocks a late
// opened/synchronize for the same PR. Out-of-order deliveries arrive
// seconds apart; a day is generous and keeps the map small.
const closedPRTombstoneTTL = 24 * time.Hour

func prKey(repo string, number int) string {
	return strings.ToLower(repo) + "#" + strconv.Itoa(number)
}

// markPRClosed records that a PR was closed, so a synchronize or opened
// that reaches us afterwards doesn't rebuild the preview.
func (d *Dispatcher) markPRClosed(repo string, number int) {
	d.closedMu.Lock()
	defer d.closedMu.Unlock()
	if d.closedPRs == nil {
		d.closedPRs = map[string]time.Time{}
	}
	now := time.Now()
	for k, at := range d.closedPRs {
		if now.Sub(at) > closedPRTombstoneTTL {
			delete(d.closedPRs, k)
		}
	}
	d.closedPRs[prKey(repo, number)] = now
}

// clearPRClosed drops the tombstone on reopen.
func (d *Dispatcher) clearPRClosed(repo string, number int) {
	d.closedMu.Lock()
	defer d.closedMu.Unlock()
	delete(d.closedPRs, prKey(repo, number))
}

func (d *Dispatcher) prClosed(repo string, number int) bool {
	d.closedMu.Lock()
	defer d.closedMu.Unlock()
	at, ok := d.closedPRs[prKey(repo, number)]
	return ok && time.Since(at) <= closedPRTombstoneTTL
}
