package leader

import (
	"context"
	"testing"
	"time"
)

func TestWaitForPreference(t *testing.T) {
	settleBeforeContest = 0
	ctx := context.Background()
	cfg := Config{LockName: "b", PreferNotHolding: "a", DeferFor: 150 * time.Millisecond}

	elapsed := func(cfg Config) time.Duration {
		start := time.Now()
		if !waitForPreference(ctx, cfg, true) {
			t.Fatal("unexpected ctx end")
		}
		return time.Since(start)
	}

	if d := elapsed(cfg); d > 100*time.Millisecond {
		t.Fatalf("not holding a: waited %v, want immediate", d)
	}
	setHeld("a", true)
	defer setHeld("a", false)
	if d := elapsed(cfg); d < 150*time.Millisecond {
		t.Fatalf("holding a with peers: waited %v, want >= DeferFor", d)
	}
	cfg.HasPeers = func() bool { return false }
	if d := elapsed(cfg); d > 100*time.Millisecond {
		t.Fatalf("holding a, no peers: waited %v, want immediate", d)
	}
}
