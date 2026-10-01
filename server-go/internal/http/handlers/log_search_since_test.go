package handlers

import (
	"testing"
	"time"
)

// since=1h parsed as zero time, so the search scanned the whole archive
// instead of the last hour.
func TestParseTs_RelativeLookback(t *testing.T) {
	t.Parallel()
	got := parseTs("1h")
	if got.IsZero() || time.Since(got) < 59*time.Minute || time.Since(got) > 61*time.Minute {
		t.Errorf("parseTs(1h) = %v, want about an hour ago", got)
	}
	if parseTs("garbage") != (time.Time{}) {
		t.Error("garbage should still parse to zero")
	}
}
