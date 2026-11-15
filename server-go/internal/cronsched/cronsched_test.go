package cronsched

import (
	"testing"
	"time"
)

func TestNext(t *testing.T) {
	t.Parallel()
	// Wednesday.
	base := time.Date(2026, 11, 11, 10, 7, 30, 0, time.UTC)
	cases := []struct {
		spec string
		want time.Time
	}{
		{"*/5 * * * *", time.Date(2026, 11, 11, 10, 10, 0, 0, time.UTC)},
		{"* * * * *", time.Date(2026, 11, 11, 10, 8, 0, 0, time.UTC)},
		{"@hourly", time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)},
		{"@daily", time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)},
		{"30 2 * * sun", time.Date(2026, 11, 15, 2, 30, 0, 0, time.UTC)},
		{"0 0 * * 7", time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)},
		{"0 9 * * 1-5", time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)},
		{"0 0 1 jan *", time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
		// dom and dow both restricted: either matches (Friday the 13th
		// rule): the 13th, a Friday, comes before the next Monday.
		{"0 0 13 * 1", time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)},
		{"15,45 8-10 * * *", time.Date(2026, 11, 11, 10, 15, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		s, err := Parse(c.spec)
		if err != nil {
			t.Fatalf("%s: %v", c.spec, err)
		}
		if got := s.Next(base); !got.Equal(c.want) {
			t.Errorf("%s: Next = %v, want %v", c.spec, got, c.want)
		}
	}
}

func TestNeverFires(t *testing.T) {
	t.Parallel()
	s, err := Parse("0 0 30 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); !got.IsZero() {
		t.Errorf("Feb 30 fired at %v", got)
	}
}

func TestInterval(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 11, 11, 10, 7, 0, 0, time.UTC)
	for spec, want := range map[string]time.Duration{
		"*/5 * * * *": 5 * time.Minute,
		"@hourly":     time.Hour,
		"0 3 * * *":   24 * time.Hour,
	} {
		s, err := Parse(spec)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Interval(base); got != want {
			t.Errorf("%s: Interval = %v, want %v", spec, got, want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"", "* * * *", "61 * * * *", "*/0 * * * *", "x * * * *", "5-1 * * * *"} {
		if _, err := Parse(spec); err == nil {
			t.Errorf("%q parsed", spec)
		}
	}
}
