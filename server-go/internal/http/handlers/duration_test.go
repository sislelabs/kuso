package handlers

import (
	"testing"
	"time"
)

// The UI range pickers ("7d", "30d") and the CLI's --since flag both
// speak day units, which time.ParseDuration does not support. Before
// parseRangeDuration existed, every d-suffixed range silently failed:
// the metrics handler 400'd and the errors handler fell back to 24h
// while the panel still claimed to show 7d.
func TestParseRangeDuration(t *testing.T) {
	tests := []struct {
		in   string
		want time.Duration
		ok   bool
	}{
		// Units time.ParseDuration already handles must keep working.
		{"1h", time.Hour, true},
		{"6h", 6 * time.Hour, true},
		{"90m", 90 * time.Minute, true},
		{"30s", 30 * time.Second, true},

		// The day unit this helper exists for.
		{"1d", 24 * time.Hour, true},
		{"7d", 7 * 24 * time.Hour, true},
		{"30d", 30 * 24 * time.Hour, true},

		// Fractional and multi-digit days.
		{"0.5d", 12 * time.Hour, true},
		{"365d", 365 * 24 * time.Hour, true},

		// Weeks, so "4w" in a future picker doesn't repeat this bug.
		{"1w", 7 * 24 * time.Hour, true},
		{"2w", 14 * 24 * time.Hour, true},

		// Rejections.
		{"", 0, false},
		{"d", 0, false},
		{"abc", 0, false},
		{"7dd", 0, false},
		{"-1d", 0, false},
		{"0d", 0, false},
		{"7 d", 0, false},
	}

	for _, tt := range tests {
		got, err := parseRangeDuration(tt.in)
		if tt.ok {
			if err != nil {
				t.Errorf("parseRangeDuration(%q) unexpected error: %v", tt.in, err)
				continue
			}
			if got != tt.want {
				t.Errorf("parseRangeDuration(%q) = %v, want %v", tt.in, got, tt.want)
			}
			continue
		}
		if err == nil {
			t.Errorf("parseRangeDuration(%q) = %v, want error", tt.in, got)
		}
	}
}

// pickStep must scale the step with the range. Its day branches were
// dead code while ParseDuration rejected "7d"/"30d" and it fell through
// to the 30s default.
func TestPickStepUsesDayRanges(t *testing.T) {
	tests := []struct{ in, want string }{
		{"1h", "30s"},
		{"6h", "2m"},
		{"1d", "10m"},
		{"7d", "1h"},
		{"30d", "6h"},
	}
	for _, tt := range tests {
		if got := pickStep(tt.in); got != tt.want {
			t.Errorf("pickStep(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// The rate() window must be at least as wide as the step, otherwise a
// 7d view samples one minute per hour and renders as a flat zero line
// even though Prometheus holds the data.
func TestRateWindowCoversStep(t *testing.T) {
	for _, rangeStr := range []string{"1h", "6h", "1d", "7d", "30d"} {
		step := pickStep(rangeStr)
		stepDur, err := time.ParseDuration(step)
		if err != nil {
			t.Fatalf("pickStep(%q) returned unparsable step %q", rangeStr, step)
		}
		win, err := time.ParseDuration(rateWindow(step))
		if err != nil {
			t.Fatalf("rateWindow(%q) unparsable: %v", step, err)
		}
		if win < stepDur {
			t.Errorf("range %s: rate window %v is narrower than step %v; long ranges will read as empty",
				rangeStr, win, stepDur)
		}
	}
}
