package handlers

import (
	"testing"
	"time"

	"kuso/server/internal/db"
)

func TestApplyResolvedHidesUntilRecurrence(t *testing.T) {
	t.Parallel()
	at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	groups := func() []db.ErrorGroup {
		return []db.ErrorGroup{
			{Fingerprint: "quiet", LastSeen: at.Add(-time.Minute)},
			{Fingerprint: "back", LastSeen: at.Add(time.Minute)},
			{Fingerprint: "open", LastSeen: at},
		}
	}
	marks := map[string]time.Time{"quiet": at, "back": at}

	got := applyResolved(groups(), marks, false)
	if len(got) != 2 || got[0].Fingerprint != "back" || got[1].Fingerprint != "open" {
		t.Fatalf("visible = %+v, want back + open", got)
	}
	if got[0].ResolvedAt == nil || got[1].ResolvedAt != nil {
		t.Fatalf("resolvedAt stamped wrong: %+v", got)
	}
	if all := applyResolved(groups(), marks, true); len(all) != 3 {
		t.Fatalf("include=true listed %d groups, want 3", len(all))
	}
}
