package serverstate

import (
	"testing"
	"time"
)

// TestPollerHealthy covers the leader-gated heartbeat logic:
// non-leader always healthy; leader warming up (no beat) healthy;
// leader with fresh beat healthy; leader with stale beat unhealthy;
// losing leadership resets the beat.
func TestPollerHealthy(t *testing.T) {
	// Not parallel — mutates package state.
	t.Cleanup(func() { SetLeading(false) })

	// Non-leader: always healthy, leading=false.
	SetLeading(false)
	if h, l := PollerHealthy(time.Second); !h || l {
		t.Errorf("non-leader: got healthy=%v leading=%v, want true,false", h, l)
	}

	// Become leader, no beat yet → warming up → healthy, leading.
	SetLeading(true)
	if h, l := PollerHealthy(time.Second); !h || !l {
		t.Errorf("leader warming: got healthy=%v leading=%v, want true,true", h, l)
	}

	// Fresh beat → healthy.
	PollerHeartbeat()
	if h, l := PollerHealthy(time.Second); !h || !l {
		t.Errorf("leader fresh beat: got healthy=%v leading=%v, want true,true", h, l)
	}

	// Force a stale beat → unhealthy.
	mu.Lock()
	pollerBeat = time.Now().Add(-time.Hour)
	mu.Unlock()
	if h, l := PollerHealthy(30 * time.Second); h || !l {
		t.Errorf("leader stale beat: got healthy=%v leading=%v, want false,true", h, l)
	}

	// Losing leadership resets the beat and returns healthy.
	SetLeading(false)
	mu.RLock()
	reset := pollerBeat.IsZero()
	mu.RUnlock()
	if !reset {
		t.Error("losing leadership should reset pollerBeat to zero")
	}
	if h, l := PollerHealthy(30 * time.Second); !h || l {
		t.Errorf("after losing leadership: got healthy=%v leading=%v, want true,false", h, l)
	}
}
