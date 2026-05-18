package notify

import (
	"testing"
	"time"
)

// TestOutboxBackoff pins the exponential-backoff curve. Numbers must
// stay roughly aligned with the production schedule so a regression
// here (e.g. accidentally capping too low) gets caught at CI time
// instead of by an operator noticing slow recovery from a Slack
// outage in production.
func TestOutboxBackoff(t *testing.T) {
	// Attempts 1..10. Each value should fit in [floor, ceil] where
	// the bounds account for ±20% jitter.
	cases := []struct {
		attempt int
		floor   time.Duration
		ceil    time.Duration
	}{
		{1, 4 * time.Second, 7 * time.Second},      // base=5s ±20%
		{2, 8 * time.Second, 13 * time.Second},     // base=10s
		{3, 16 * time.Second, 25 * time.Second},    // base=20s
		{4, 32 * time.Second, 49 * time.Second},    // base=40s
		{5, 64 * time.Second, 97 * time.Second},    // base=80s
		{6, 128 * time.Second, 193 * time.Second},  // base=160s
		// Past this point the curve caps at outboxBackoffMax (5 min)
		// because base*2^attempt exceeds the cap. With ±20% jitter
		// the floor is 4 min, the ceil is 6 min.
		{7, 4 * time.Minute, 6 * time.Minute},
		{10, 4 * time.Minute, 6 * time.Minute},
	}
	for _, tc := range cases {
		// Run a few iterations to amortise the jitter so the test
		// fails on a systematically-wrong base, not on a single
		// unlucky jitter draw.
		for i := 0; i < 20; i++ {
			d := outboxBackoff(tc.attempt)
			if d < tc.floor || d > tc.ceil {
				t.Errorf("attempt=%d: backoff=%v out of [%v, %v]",
					tc.attempt, d, tc.floor, tc.ceil)
				break
			}
		}
	}
}

// TestOutboxBackoffZeroAttempt — defensive: passing 0 or negative
// shouldn't crash, should treat as attempt=1.
func TestOutboxBackoffZeroAttempt(t *testing.T) {
	for _, n := range []int{-5, -1, 0} {
		d := outboxBackoff(n)
		if d < 4*time.Second || d > 7*time.Second {
			t.Errorf("attempt=%d: backoff=%v should clamp to attempt=1 window", n, d)
		}
	}
}
