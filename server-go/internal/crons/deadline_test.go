package crons

import (
	"context"
	"testing"
	"time"
)

// Under the default Forbid policy a hung run with no deadline skips every
// later schedule silently, so a new cron gets one unless it asks.
func TestAdd_DefaultsActiveDeadline(t *testing.T) {
	cases := []struct {
		schedule string
		asked    int
		want     int
	}{
		{"*/5 * * * *", 0, 3600},  // 10x5m is under the 1h floor
		{"0 */2 * * *", 0, 72000}, // 10x2h
		{"0 3 * * *", 0, 86400},   // 10x24h, capped at 24h
		{"*/5 * * * *", 120, 120}, // an explicit deadline wins
	}
	for _, c := range cases {
		s := egressFixture(t, false, false)
		cr, err := s.Add(context.Background(), "alpha", "web", CreateCronRequest{
			Name: "job", Schedule: c.schedule, Command: []string{"echo"}, ActiveDeadlineSeconds: c.asked,
		})
		if err != nil {
			t.Fatalf("%s: Add: %v", c.schedule, err)
		}
		if cr.Spec.ActiveDeadlineSeconds != c.want {
			t.Errorf("%s (asked %d): activeDeadlineSeconds = %d, want %d", c.schedule, c.asked, cr.Spec.ActiveDeadlineSeconds, c.want)
		}
	}
}

func TestDefaultActiveDeadline_UnparseableFallsBackTo24h(t *testing.T) {
	if got := defaultActiveDeadline(0, "@reboot", time.Now()); got != 86400 {
		t.Errorf("got %d, want 86400", got)
	}
}
