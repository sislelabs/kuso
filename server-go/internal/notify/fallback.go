package notify

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// Platform-class events are the ones that report the control plane
// itself breaking, which includes the Postgres the outbox lives in. When
// the outbox write fails they are sent straight to the channels from the
// last channel list this process read successfully.
var directFallbackTypes = map[EventType]bool{
	EventPlatformUnhealthy: true,
	EventPlatformRecovered: true,
	EventIngressDown:       true,
	EventAlertEngineDown:   true,
	EventNodeUnreachable:   true,
	EventNodeRecovered:     true,
}

// directFallbackAttempts / directFallbackBackoff bound one direct send.
// The emitters are once-a-minute loops, so the worst case (all attempts
// time out on the 8s HTTP client) still finishes inside a tick.
var (
	directFallbackAttempts = 3
	directFallbackBackoff  = 2 * time.Second
)

func directFallbackEligible(e Event) bool { return directFallbackTypes[e.Type] }

// rememberChannels keeps a copy of a successful channel read that does
// not expire, so a DB outage longer than notifsCacheTTL still knows where
// to page.
func (d *Dispatcher) rememberChannels(n []db.Notification) {
	d.lastGoodMu.Lock()
	d.lastGood = n
	d.lastGoodMu.Unlock()
	// Off the caller's path: it holds the channel-cache lock.
	go d.saveSnapshot()
}

func (d *Dispatcher) lastGoodChannels() []db.Notification {
	d.lastGoodMu.Lock()
	defer d.lastGoodMu.Unlock()
	return d.lastGood
}

// deliverDirect sends e to every admitting channel from memory with a
// bounded retry and reports success if at least one channel took it.
// Mutes are not consulted: they live in the same unreachable DB, and a
// platform event is never project-scoped anyway.
func (d *Dispatcher) deliverDirect(parent context.Context, e Event) error {
	var targets []db.Notification
	for _, n := range d.lastGoodChannels() {
		if channelAdmits(n, e) {
			targets = append(targets, n)
		}
	}
	if len(targets) == 0 {
		return errors.New("notify: no remembered channel admits this event")
	}
	send := d.directSendFn
	if send == nil {
		send = d.deliverViaChannel
	}
	var (
		wg sync.WaitGroup
		mu sync.Mutex
		ok int
		le error
	)
	for _, n := range targets {
		wg.Add(1)
		go func(n db.Notification) {
			defer wg.Done()
			var err error
			for attempt := 1; attempt <= directFallbackAttempts; attempt++ {
				if err = send(parent, n, e); err == nil {
					break
				}
				if attempt < directFallbackAttempts && !sleepWithCtx(parent, directFallbackBackoff*time.Duration(attempt)) {
					break
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				ok++
				return
			}
			le = err
			if d.logger != nil {
				d.logger.Warn("notify: direct fallback delivery failed", "channel", n.ID, "type", string(e.Type), "err", err)
			}
		}(n)
	}
	wg.Wait()
	if ok == 0 {
		return fmt.Errorf("notify: direct fallback: %w", le)
	}
	metricsDirectFallback.WithLabelValues(string(e.Type)).Add(float64(ok))
	return nil
}
