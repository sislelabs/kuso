package kusoCli

import (
	"strings"
	"testing"
	"time"
)

func TestTriggerResultLine(t *testing.T) {
	now := time.Date(2026, 9, 28, 15, 45, 0, 0, time.UTC)

	fresh := `{"id":"e2e-api-46fe4156ba59-mulh","branch":"staging","status":"pending","existing":false}`
	line, id, err := triggerResultLine([]byte(fresh), now)
	if err != nil {
		t.Fatal(err)
	}
	if id != "e2e-api-46fe4156ba59-mulh" || line != "build e2e-api-46fe4156ba59-mulh started (branch=staging, status=pending)" {
		t.Errorf("fresh: id=%q line=%q", id, line)
	}

	coalesced := `{"id":"e2e-web-46fe4156ba59","branch":"staging","status":"running","startedAt":"2026-09-28T15:40:19Z","existing":true}`
	line, id, err = triggerResultLine([]byte(coalesced), now)
	if err != nil {
		t.Fatal(err)
	}
	want := "build e2e-web-46fe4156ba59 already in progress (started 4m ago) — not starting another"
	if id != "e2e-web-46fe4156ba59" || line != want {
		t.Errorf("coalesced: id=%q line=%q, want %q", id, line, want)
	}

	// Pending builds have no startedAt yet; fall back to createdAt.
	pending := `{"id":"x","branch":"main","status":"pending","createdAt":"2026-09-28T15:44:30Z","existing":true}`
	line, _, _ = triggerResultLine([]byte(pending), now)
	if !strings.Contains(line, "(started 30s ago)") {
		t.Errorf("pending coalesced: %q", line)
	}

	// Neither timestamp: no bogus age.
	bare := `{"id":"x","branch":"main","status":"queued","existing":true}`
	line, _, _ = triggerResultLine([]byte(bare), now)
	if line != "build x already in progress — not starting another" {
		t.Errorf("bare coalesced: %q", line)
	}
}
