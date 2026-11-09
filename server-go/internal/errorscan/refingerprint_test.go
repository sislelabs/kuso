package errorscan

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// Rows stored under the old fingerprinting (one group per timestamp
// variant, bare traceback titles) merge into one group after the rewrite.
func TestRefingerprintMergesOldSplitGroups(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	now := time.Now()
	old := []db.ErrorEvent{
		{Fingerprint: "old1", Message: "2026-11-08T19:40:05Z ERROR db timeout after 250ms", RawLine: "2026-11-08T19:40:05Z ERROR db timeout after 250ms"},
		{Fingerprint: "old2", Message: "2026-11-08T19:41:05Z ERROR db timeout after 300ms", RawLine: "2026-11-08T19:41:05Z ERROR db timeout after 300ms"},
		{Fingerprint: "old3", Message: "Traceback (most recent call last):", RawLine: "Traceback (most recent call last):\n  File \"w.py\", line 9, in run\nKeyError: 'cart'"},
	}
	for i, e := range old {
		e.Project, e.Service, e.Env, e.Pod = "proj", "proj-svc", "production", "pod-1"
		e.Ts = now.Add(time.Duration(i) * time.Second)
		if err := d.InsertErrorEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	s := &Scanner{DB: d, Logger: slog.Default()}
	s.applyDefaults()
	if err := s.refingerprintStep(ctx); err != nil {
		t.Fatal(err)
	}
	groups, err := d.ListErrorGroups(ctx, "proj", "proj-svc", "", now.Add(-time.Hour), 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2: %+v", len(groups), groups)
	}
	titles := map[string]int64{}
	for _, g := range groups {
		titles[g.Message] = g.Count
	}
	if titles["db timeout after 250ms"]+titles["db timeout after 300ms"] != 2 {
		t.Errorf("timeout variants not merged: %+v", titles)
	}
	if titles["KeyError: 'cart'"] != 1 {
		t.Errorf("traceback not retitled: %+v", titles)
	}
	// A second step is a no-op once done.
	if err := s.refingerprintStep(ctx); err != nil {
		t.Fatal(err)
	}
}
