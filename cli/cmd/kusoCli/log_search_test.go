package kusoCli

import (
	"testing"
	"time"
)

func TestParseSinceFlag(t *testing.T) {
	now := time.Now()
	near := func(got, want time.Time) bool {
		d := got.Sub(want)
		return d > -time.Minute && d < time.Minute
	}
	relative := map[string]time.Duration{
		"90m": 90 * time.Minute,
		"2h":  2 * time.Hour,
		"1d":  24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"2w":  14 * 24 * time.Hour,
	}
	for in, ago := range relative {
		got, err := parseSinceFlag(in)
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !near(got, now.Add(-ago)) {
			t.Errorf("%q: got %s, want ~%s", in, got, now.Add(-ago))
		}
	}

	absolute := map[string]time.Time{
		"2026-09-22T08:00:00Z": time.Date(2026, 9, 22, 8, 0, 0, 0, time.UTC),
		"2026-09-22":           time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC),
		"1758528000":           time.Unix(1758528000, 0),
	}
	for in, want := range absolute {
		got, err := parseSinceFlag(in)
		if err != nil || !got.Equal(want) {
			t.Errorf("%q: got %s (%v), want %s", in, got, err, want)
		}
	}

	// Each of these used to parse as a unix second near 1970.
	for _, in := range []string{"7x", "7 d", "7dd", "-1d", "0d", "12abc"} {
		if got, err := parseSinceFlag(in); err == nil {
			t.Errorf("%q: want error, got %s", in, got)
		}
	}
}
