package alerts

import (
	"fmt"
	"log/slog"
	"testing"
	"time"
)

// Guards the throttle fallback: MarkAlertFired's error used to be
// discarded, so a DB blip meant LastFiredAt never advanced and the rule
// re-fired every tick. The in-memory stamp must keep throttling alive
// while the DB is down, and the map must stay bounded.

func TestThrottleMemRecordAndRead(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slog.Default()}

	if _, ok := e.lastFiredMem("r1"); ok {
		t.Fatal("empty engine reported a stamp")
	}
	now := time.Now().UTC()
	e.recordFiredMem("r1", now)
	got, ok := e.lastFiredMem("r1")
	if !ok || !got.Equal(now) {
		t.Fatalf("stamp round-trip: got %v ok=%v", got, ok)
	}
	// Re-record advances.
	later := now.Add(time.Minute)
	e.recordFiredMem("r1", later)
	if got, _ := e.lastFiredMem("r1"); !got.Equal(later) {
		t.Errorf("re-record did not advance: %v", got)
	}

	// Once the durable stamp lands the fallback must clear — otherwise
	// it shadows an operator backdating LastFiredAt to force a re-fire.
	e.dropFiredMem("r1")
	if _, ok := e.lastFiredMem("r1"); ok {
		t.Error("drop did not clear the fallback stamp")
	}
	// Dropping an absent key is a no-op, not a panic.
	e.dropFiredMem("never-recorded")
}

func TestThrottleMemBounded(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slog.Default()}
	now := time.Now().UTC()

	// Fill past the cap with fresh entries; the map must never exceed
	// the bound.
	for i := 0; i < lastFiredMaxEntries+100; i++ {
		e.recordFiredMem(fmt.Sprintf("rule-%d", i), now.Add(time.Duration(i)*time.Second))
	}
	e.mu.Lock()
	n := len(e.lastFired)
	e.mu.Unlock()
	if n > lastFiredMaxEntries {
		t.Errorf("map grew to %d entries, cap is %d", n, lastFiredMaxEntries)
	}
}

func TestThrottleMemEvictsStaleFirst(t *testing.T) {
	t.Parallel()
	e := &Engine{Logger: slog.Default()}
	now := time.Now().UTC()

	// One stale entry (older than 24h) among fresh ones: at capacity,
	// the stale one goes and the fresh survivors keep their stamps.
	e.recordFiredMem("stale", now.Add(-48*time.Hour))
	for i := 0; i < lastFiredMaxEntries; i++ {
		e.recordFiredMem(fmt.Sprintf("fresh-%d", i), now)
	}
	if _, ok := e.lastFiredMem("stale"); ok {
		t.Error("stale entry survived eviction at capacity")
	}
	if _, ok := e.lastFiredMem("fresh-0"); !ok {
		t.Error("fresh entry was evicted while a stale one existed")
	}
}
