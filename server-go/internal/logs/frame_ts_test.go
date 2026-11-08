package logs

import (
	"testing"
	"time"
)

func TestFrameTimestamp(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ts, text := frameTimestamp("2026-11-08T10:00:01.123456789Z hello world", now)
	if ts != "2026-11-08T10:00:01.123456789Z" || text != "hello world" {
		t.Errorf("kubelet prefix: got %q %q", ts, text)
	}
	ts, text = frameTimestamp("no timestamp here", now)
	if ts != "2026-11-08T12:00:00Z" || text != "no timestamp here" {
		t.Errorf("fallback: got %q %q", ts, text)
	}
}
