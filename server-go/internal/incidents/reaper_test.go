package incidents

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"kuso/server/internal/db"
)

// fakeReaperStore mimics the open-set semantics of the real DB: an
// incident is "open" (occupies a MaxConcurrent slot) iff its state is
// non-terminal. StaleInvestigatingIncidents filters on state +
// createdAt like the real query.
type fakeReaperStore struct {
	incidents map[string]*db.Incident
	feedback  map[string][]db.IncidentFeedback
	listErr   error
}

func newFakeReaperStore(ins ...db.Incident) *fakeReaperStore {
	s := &fakeReaperStore{incidents: map[string]*db.Incident{}, feedback: map[string][]db.IncidentFeedback{}}
	for i := range ins {
		in := ins[i]
		s.incidents[in.ID] = &in
	}
	return s
}

func (s *fakeReaperStore) StaleInvestigatingIncidents(_ context.Context, cutoff time.Time) ([]db.Incident, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	var out []db.Incident
	for _, in := range s.incidents {
		if in.State == db.IncidentInvestigating && in.CreatedAt.Before(cutoff) {
			out = append(out, *in)
		}
	}
	return out, nil
}

func (s *fakeReaperStore) SetIncidentState(_ context.Context, id, state string) error {
	in, ok := s.incidents[id]
	if !ok {
		return db.ErrIncidentNotFound
	}
	in.State = state
	return nil
}

func (s *fakeReaperStore) AppendIncidentFeedback(_ context.Context, id string, fb db.IncidentFeedback) error {
	s.feedback[id] = append(s.feedback[id], fb)
	return nil
}

// openCount mirrors db.CountOpenIncidents (state in the open set).
func (s *fakeReaperStore) openCount() int {
	open := map[string]bool{}
	for _, st := range db.IncidentOpenStates {
		open[st] = true
	}
	n := 0
	for _, in := range s.incidents {
		if open[in.State] {
			n++
		}
	}
	return n
}

// TestReapStuck_TimesOutAndReleasesSlot is the H6 regression: an agent
// Job that crashed before posting findings left its incident in
// "investigating" forever, permanently consuming a MaxConcurrent slot
// and attaching all future events to the corpse. The reaper must move
// it to timed_out (terminal → slot freed → decide() spawns again).
func TestReapStuck_TimesOutAndReleasesSlot(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	const timeout = 2 * time.Hour

	stuck := db.Incident{
		ID: "inc-stuck", State: db.IncidentInvestigating,
		TargetKey: "pod.crashed|p|s", Title: "web crashed",
		CreatedAt: now.Add(-3 * time.Hour), // past timeout
	}
	fresh := db.Incident{
		ID: "inc-fresh", State: db.IncidentInvestigating,
		TargetKey: "pod.crashed|p|other", CreatedAt: now.Add(-10 * time.Minute),
	}
	awaiting := db.Incident{
		ID: "inc-await", State: db.IncidentAwaitingFeedback,
		TargetKey: "alert.fired|p|s", CreatedAt: now.Add(-48 * time.Hour), // old but NOT investigating
	}
	store := newFakeReaperStore(stuck, fresh, awaiting)

	if got := store.openCount(); got != 3 {
		t.Fatalf("precondition: open = %d, want 3", got)
	}

	if n := reapStuck(context.Background(), store, timeout, now, slog.Default()); n != 1 {
		t.Fatalf("reaped %d, want exactly 1 (only the stuck investigation)", n)
	}

	// The stuck incident is terminal → slot released.
	if st := store.incidents["inc-stuck"].State; st != db.IncidentTimedOut {
		t.Errorf("stuck incident state = %q, want %q", st, db.IncidentTimedOut)
	}
	if got := store.openCount(); got != 2 {
		t.Errorf("open after reap = %d, want 2 (slot must be released)", got)
	}
	// A trail entry explains the close.
	if len(store.feedback["inc-stuck"]) == 0 {
		t.Error("expected a timeout feedback entry on the reaped incident")
	}

	// Untouched: a fresh investigation and an old-but-progressing one.
	if st := store.incidents["inc-fresh"].State; st != db.IncidentInvestigating {
		t.Errorf("fresh incident was reaped (state=%q) — timeout must not hit in-flight runs", st)
	}
	if st := store.incidents["inc-await"].State; st != db.IncidentAwaitingFeedback {
		t.Errorf("awaiting_feedback incident was reaped (state=%q) — reaper must only touch investigating", st)
	}

	// With the slot free, decide() must SPAWN for the same target again
	// instead of attaching — this is the "jammed forever" half of the
	// finding. (No open incident for the target, no cooldown record.)
	got := decide(false, time.Time{}, false, store.openCount(), 3, time.Hour, now)
	if got != decideSpawn {
		t.Errorf("post-reap decide = %v, want decideSpawn (freed slot must allow a respawn)", got)
	}

	// Idempotent: a second pass finds nothing (state is terminal).
	if n := reapStuck(context.Background(), store, timeout, now, slog.Default()); n != 0 {
		t.Errorf("second reap pass reaped %d, want 0", n)
	}
}

// TestReapStuck_ListErrorIsSafe: a transient DB error must not panic or
// transition anything.
func TestReapStuck_ListErrorIsSafe(t *testing.T) {
	t.Parallel()
	store := newFakeReaperStore(db.Incident{
		ID: "inc-1", State: db.IncidentInvestigating, CreatedAt: time.Now().Add(-24 * time.Hour),
	})
	store.listErr = context.DeadlineExceeded
	if n := reapStuck(context.Background(), store, time.Hour, time.Now(), slog.Default()); n != 0 {
		t.Errorf("reaped %d on list error, want 0", n)
	}
	if st := store.incidents["inc-1"].State; st != db.IncidentInvestigating {
		t.Errorf("state changed on list error: %q", st)
	}
}
