package updater

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"kuso/server/internal/notify"
)

type memSettings map[string]string

func (m memSettings) GetSetting(_ context.Context, k string) (string, error) { return m[k], nil }
func (m memSettings) SetSetting(_ context.Context, k, v, _ string) error {
	m[k] = v
	return nil
}

type recEmitter struct{ events []notify.Event }

func (r *recEmitter) Emit(e notify.Event) { r.events = append(r.events, e) }

func TestMaybeNotifyAvailable_OncePerVersion(t *testing.T) {
	t.Parallel()
	em := &recEmitter{}
	s := &Service{Notify: em, settings: memSettings{}}
	ctx := context.Background()

	st := State{Current: "v0.27.4", Latest: "v0.27.5", NeedsUpdate: true}
	s.maybeNotifyAvailable(ctx, st)
	s.maybeNotifyAvailable(ctx, st)
	if len(em.events) != 1 {
		t.Fatalf("got %d events for one version, want 1", len(em.events))
	}
	if em.events[0].Type != notify.EventUpdateAvailable {
		t.Errorf("type = %q", em.events[0].Type)
	}

	st.Latest = "v0.27.6"
	s.maybeNotifyAvailable(ctx, st)
	if len(em.events) != 2 {
		t.Fatalf("a newer version should notify again, got %d events", len(em.events))
	}
}

func TestMaybeNotifyAvailable_SilentWhenUpToDate(t *testing.T) {
	t.Parallel()
	em := &recEmitter{}
	s := &Service{Notify: em, settings: memSettings{}}
	s.maybeNotifyAvailable(context.Background(), State{Current: "v0.27.5", Latest: "v0.27.5"})
	if len(em.events) != 0 {
		t.Fatalf("emitted %d events while up to date", len(em.events))
	}
}

func TestNotifyOutcome_OncePerRunAndNotStale(t *testing.T) {
	t.Parallel()
	em := &recEmitter{}
	s := &Service{Notify: em, settings: memSettings{}}
	ctx := context.Background()
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	started := now.Add(-10 * time.Minute)

	failed := UpdateStatus{Phase: "rolled-back", Message: "server never Ready", Started: started, Updated: now}
	s.notifyOutcome(ctx, failed, now)
	s.notifyOutcome(ctx, failed, now.Add(time.Minute))
	if len(em.events) != 1 || em.events[0].Type != notify.EventUpdateFailed {
		t.Fatalf("want one update.failed, got %+v", em.events)
	}

	partial := UpdateStatus{Phase: "done", Message: "upgraded to v1 with warnings: RBAC owed", Started: started.Add(time.Hour), Updated: now}
	s.notifyOutcome(ctx, partial, now)
	if len(em.events) != 2 || em.events[1].Type != notify.EventUpdatePartial {
		t.Fatalf("want update.partial, got %+v", em.events)
	}

	clean := UpdateStatus{Phase: "done", Message: "upgraded to v2", Started: started.Add(2 * time.Hour), Updated: now}
	s.notifyOutcome(ctx, clean, now)
	inFlight := UpdateStatus{Phase: "rolling-server", Started: started.Add(3 * time.Hour), Updated: now}
	s.notifyOutcome(ctx, inFlight, now)
	// A failure first seen long after it happened (boot after upgrade) is not replayed.
	stale := UpdateStatus{Phase: "failed", Started: now.Add(-48 * time.Hour), Updated: now.Add(-47 * time.Hour)}
	s.notifyOutcome(ctx, stale, now)
	if len(em.events) != 2 {
		t.Fatalf("clean / in-flight / stale outcomes must not notify, got %+v", em.events[2:])
	}
}

func TestNotifyRejected_OncePerVersion(t *testing.T) {
	t.Parallel()
	em := &recEmitter{}
	s := &Service{Notify: em, settings: memSettings{}}
	ctx := context.Background()
	s.notifyRejected(ctx, "v9.0.0", "signature does not match")
	s.notifyRejected(ctx, "v9.0.0", "signature does not match")
	if len(em.events) != 1 || em.events[0].Type != notify.EventUpdateRejected || em.events[0].Severity != "error" {
		t.Fatalf("want one update.rejected, got %+v", em.events)
	}
}

// A poll refused on verification records the tag so tick can raise
// update.rejected.
func TestTickNotifiesRejectedRelease(t *testing.T) {
	t.Setenv("KUSO_REQUIRE_SIGNATURES", "true")
	em := &recEmitter{}
	ghRelease := `{"tag_name":"v8.8.8","body":"n","assets":[]}`
	s := &Service{
		Repo: "sislelabs/kuso", Current: "v8.8.7", Notify: em, settings: memSettings{},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{Transport: rtFunc(func(r *http.Request) (*http.Response, error) {
			return jsonResp(200, ghRelease), nil
		})},
	}
	s.tick(context.Background())
	if len(em.events) != 1 || em.events[0].Type != notify.EventUpdateRejected {
		t.Fatalf("want update.rejected for an unsigned release, got %+v", em.events)
	}
}
