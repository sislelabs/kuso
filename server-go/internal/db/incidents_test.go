package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestIncidentLifecycle(t *testing.T) {
	d := openTestDB(t) // skips without KUSO_TEST_PG_DSN
	ctx := context.Background()

	in := Incident{
		ID:          "inc-1",
		EventType:   "pod.crashed",
		Project:     "berivangold",
		Service:     "web",
		TargetKey:   "pod.crashed|berivangold|web",
		State:       IncidentInvestigating,
		Title:       "web crashed",
		Severity:    "error",
		ContextPack: json.RawMessage(`{"restarts":4}`),
		AgentToken:  "tok-abc",
	}
	if err := d.CreateIncident(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Dedup: an open incident for this target is found.
	got, err := d.OpenIncidentForTarget(ctx, in.TargetKey)
	if err != nil {
		t.Fatalf("open-for-target: %v", err)
	}
	if got.ID != "inc-1" || got.AgentToken != "tok-abc" {
		t.Errorf("round-trip wrong: %+v", got)
	}

	// Concurrency count sees the one open incident.
	if n, _ := d.CountOpenIncidents(ctx); n != 1 {
		t.Errorf("open count = %d, want 1", n)
	}

	// Findings → awaiting_feedback.
	if err := d.SetIncidentFindings(ctx, "inc-1", "root cause: migration lock", IncidentAwaitingFeedback); err != nil {
		t.Fatalf("findings: %v", err)
	}

	// Append two feedback entries (atomic jsonb append).
	_ = d.AppendIncidentFeedback(ctx, "inc-1", IncidentFeedback{Text: "wrong, it's X"})
	_ = d.AppendIncidentFeedback(ctx, "inc-1", IncidentFeedback{Decision: "go"})
	cur, _ := d.GetIncident(ctx, "inc-1")
	if len(cur.Feedback) != 2 || cur.Feedback[0].Text != "wrong, it's X" || cur.Feedback[1].Decision != "go" {
		t.Errorf("feedback log wrong: %+v", cur.Feedback)
	}

	// PR open.
	if err := d.SetIncidentPR(ctx, "inc-1", "https://github.com/o/r/pull/5", 5); err != nil {
		t.Fatalf("set pr: %v", err)
	}
	cur, _ = d.GetIncident(ctx, "inc-1")
	if cur.State != IncidentPROpen || cur.PRNumber != 5 {
		t.Errorf("pr state wrong: state=%s pr=%d", cur.State, cur.PRNumber)
	}
	// Still counts as open while pr_open.
	if n, _ := d.CountOpenIncidents(ctx); n != 1 {
		t.Errorf("pr_open should count as open, got %d", n)
	}

	// Resolve → terminal, closedAt stamped, no longer open.
	if err := d.SetIncidentState(ctx, "inc-1", IncidentResolved); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	cur, _ = d.GetIncident(ctx, "inc-1")
	if cur.ClosedAt == nil {
		t.Error("closedAt not stamped on resolve")
	}
	if _, err := d.OpenIncidentForTarget(ctx, in.TargetKey); err != ErrIncidentNotFound {
		t.Errorf("resolved incident must not be open, got %v", err)
	}
	if n, _ := d.CountOpenIncidents(ctx); n != 0 {
		t.Errorf("open count after resolve = %d, want 0", n)
	}

	// Cooldown: LastClosedAtForTarget now returns a time.
	at, ok, err := d.LastClosedAtForTarget(ctx, in.TargetKey)
	if err != nil || !ok {
		t.Fatalf("last-closed: ok=%v err=%v", ok, err)
	}
	if time.Since(at) > time.Minute {
		t.Errorf("closedAt looks stale: %v", at)
	}
}

// TestIncidentTimedOutReaping covers the reaper's DB contract (H6): a
// stuck "investigating" incident is found by StaleInvestigatingIncidents,
// and moving it to timed_out is TERMINAL — closedAt stamped, target no
// longer open, MaxConcurrent slot released.
func TestIncidentTimedOutReaping(t *testing.T) {
	d := openTestDB(t) // skips without KUSO_TEST_PG_DSN
	ctx := context.Background()

	in := Incident{
		ID: "inc-reap", EventType: "pod.crashed", Project: "p", Service: "s",
		TargetKey: "pod.crashed|p|s", State: IncidentInvestigating,
		Title: "crashed and the agent died", Severity: "error",
		ContextPack: json.RawMessage(`{}`), AgentToken: "tok",
	}
	if err := d.CreateIncident(ctx, in); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A cutoff in the past does NOT match the just-created incident.
	stale, err := d.StaleInvestigatingIncidents(ctx, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatalf("stale (past cutoff): %v", err)
	}
	for _, s := range stale {
		if s.ID == "inc-reap" {
			t.Error("fresh incident matched a past cutoff — timeout window is inverted")
		}
	}

	// A future cutoff matches it (simulates the incident aging past the
	// timeout without sleeping in the test).
	stale, err = d.StaleInvestigatingIncidents(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("stale (future cutoff): %v", err)
	}
	found := false
	for _, s := range stale {
		if s.ID == "inc-reap" {
			found = true
		}
	}
	if !found {
		t.Fatal("stuck investigating incident not returned by StaleInvestigatingIncidents")
	}

	// timed_out is terminal: closedAt stamped, slot released.
	if err := d.SetIncidentState(ctx, "inc-reap", IncidentTimedOut); err != nil {
		t.Fatalf("timeout transition: %v", err)
	}
	cur, _ := d.GetIncident(ctx, "inc-reap")
	if cur.State != IncidentTimedOut || cur.ClosedAt == nil {
		t.Errorf("state=%q closedAt=%v, want timed_out + stamped closedAt", cur.State, cur.ClosedAt)
	}
	if _, err := d.OpenIncidentForTarget(ctx, in.TargetKey); err != ErrIncidentNotFound {
		t.Errorf("timed-out incident must not be open for its target, got %v", err)
	}
	if n, _ := d.CountOpenIncidents(ctx); n != 0 {
		t.Errorf("open count after timeout = %d, want 0 (slot must be released)", n)
	}
	// And it no longer matches the stale query (idempotence).
	stale, _ = d.StaleInvestigatingIncidents(ctx, time.Now().Add(time.Hour))
	for _, s := range stale {
		if s.ID == "inc-reap" {
			t.Error("timed-out incident still returned as stale")
		}
	}
}

func TestIncidentNotFound(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.GetIncident(context.Background(), "nope"); err != ErrIncidentNotFound {
		t.Errorf("want ErrIncidentNotFound, got %v", err)
	}
}
