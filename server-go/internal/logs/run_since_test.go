package logs

import (
	"strings"
	"testing"
	"time"
)

// CLI-06: kubelet's sinceTime has one-second granularity, so the run-log
// page must drop the earlier lines of that second itself, keep lines AT the
// cursor (the client skips the ones it already printed) and return the
// OLDEST max lines after it, not the newest.
func TestScanRunLinesSince(t *testing.T) {
	raw := strings.Join([]string{
		"2026-11-04T10:00:00.100000000Z before",
		"2026-11-04T10:00:00.500000000Z at-cursor",
		"2026-11-04T10:00:00.700000000Z after-1",
		"2026-11-04T10:00:01.000000000Z after-2",
		"2026-11-04T10:00:02.000000000Z after-3",
	}, "\n")
	since := time.Date(2026, 11, 4, 10, 0, 0, 500000000, time.UTC)
	got := scanRunLines(strings.NewReader(raw), "run-pod", since, 3)
	var text []string
	for _, l := range got {
		text = append(text, l.Line)
	}
	if strings.Join(text, ",") != "at-cursor,after-1,after-2" {
		t.Fatalf("lines = %v", text)
	}
	if got[0].TS != "2026-11-04T10:00:00.5Z" || got[0].Pod != "run-pod" {
		t.Fatalf("first line = %+v, want ts + pod set", got[0])
	}
}
