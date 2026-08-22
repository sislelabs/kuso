package handlers

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseRangeDuration parses a user-facing range/since string, extending
// time.ParseDuration with the day and week units it does not support.
//
// This exists because every range picker in the UI offers "7d"/"30d"
// and time.ParseDuration rejects them outright ("unknown unit d").
// Callers that used ParseDuration directly either 400'd on those
// ranges (metrics) or silently swallowed the error and kept a 24h
// default while the panel still claimed to show 7d (errors).
//
// Only a bare "<number>d" / "<number>w" is special-cased. Compound
// forms like "1d12h" are deliberately not supported: nothing in the UI
// emits them, and hand-rolling a full duration grammar to accept them
// would be a much larger surface to get wrong.
func parseRangeDuration(s string) (time.Duration, error) {
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	if d, err := time.ParseDuration(s); err == nil {
		if d <= 0 {
			return 0, fmt.Errorf("duration %q must be positive", s)
		}
		return d, nil
	}

	unit := time.Duration(0)
	switch {
	case strings.HasSuffix(s, "d"):
		unit = 24 * time.Hour
	case strings.HasSuffix(s, "w"):
		unit = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q", s)
	}

	// ParseFloat rather than Atoi so "0.5d" works, and because it
	// rejects the whitespace and trailing junk ("7 d", "7dd") that a
	// hand-rolled scan would otherwise let through.
	n, err := strconv.ParseFloat(strings.TrimSuffix(s, s[len(s)-1:]), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	if n <= 0 {
		return 0, fmt.Errorf("duration %q must be positive", s)
	}
	return time.Duration(n * float64(unit)), nil
}

// rateWindow returns the PromQL rate() window to pair with a
// query_range step.
//
// PromQL evaluates rate() over a window that ends at each step point,
// so a window narrower than the step leaves gaps: at a 1h step, a
// rate(...[1m]) reads one minute per hour and every point that lands
// between scrapes evaluates to zero. That rendered the 7d and 30d
// views as flat empty lines even with the data present in Prometheus.
//
// The window is 4x the step, the usual guidance, so it always spans
// several scrapes (15s interval) and tolerates a missed one.
func rateWindow(step string) string {
	d, err := time.ParseDuration(step)
	if err != nil || d <= 0 {
		return "5m"
	}
	w := 4 * d
	if w < time.Minute {
		w = time.Minute
	}
	// Whole minutes keep the query readable; sub-minute precision has
	// no meaning against a 15s scrape interval anyway.
	if w >= time.Minute {
		w = w.Round(time.Minute)
	}
	return w.String()
}
