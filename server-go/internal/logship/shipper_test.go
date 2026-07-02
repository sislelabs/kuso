package logship

import (
	"log/slog"
	"testing"
	"time"
)

func TestResolveRetention(t *testing.T) {
	cases := []struct {
		name string
		env  string
		set  bool
		want time.Duration
	}{
		{"unset uses default", "", false, Retention},
		{"empty uses default", "", true, Retention},
		{"valid value", "3", true, 3 * 24 * time.Hour},
		{"one day min", "1", true, 1 * 24 * time.Hour},
		{"clamped to 90", "365", true, 90 * 24 * time.Hour},
		{"zero falls back", "0", true, Retention},
		{"negative falls back", "-5", true, Retention},
		{"garbage falls back", "abc", true, Retention},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv("KUSO_LOG_RETENTION_DAYS", c.env)
			} else {
				// Ensure a leaked value from the host doesn't skew the default case.
				t.Setenv("KUSO_LOG_RETENTION_DAYS", "")
			}
			if got := resolveRetention(); got != c.want {
				t.Fatalf("resolveRetention()=%v want %v", got, c.want)
			}
		})
	}
}

func TestResolveRateCap(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int
	}{
		{"unset uses default", "", rateMaxLinesPerService},
		{"valid value", "100", 100},
		{"zero falls back", "0", rateMaxLinesPerService},
		{"negative falls back", "-1", rateMaxLinesPerService},
		{"garbage falls back", "x", rateMaxLinesPerService},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN", c.env)
			if got := resolveRateCap(); got != c.want {
				t.Fatalf("resolveRateCap()=%d want %d", got, c.want)
			}
		})
	}
}

func TestAllowLineRateCap(t *testing.T) {
	t.Setenv("KUSO_LOG_MAX_LINES_PER_MIN", "3")
	s := &Shipper{Logger: slog.Default()}

	// First 3 lines for a labelled service are accepted, the rest dropped.
	for i := 0; i < 3; i++ {
		if !s.allowLine("proj", "svc") {
			t.Fatalf("line %d should be allowed under cap of 3", i)
		}
	}
	if s.allowLine("proj", "svc") {
		t.Fatal("4th line should be dropped once cap is hit")
	}
	if s.rateDropped["proj/svc"] != 1 {
		t.Fatalf("expected 1 dropped, got %d", s.rateDropped["proj/svc"])
	}

	// A different service has its own independent budget.
	if !s.allowLine("proj", "other") {
		t.Fatal("distinct service should not share the capped service's budget")
	}

	// Unlabelled (no service) is never capped.
	for i := 0; i < 10; i++ {
		if !s.allowLine("proj", "") {
			t.Fatal("unlabelled lines must never be rate-capped")
		}
	}
}
