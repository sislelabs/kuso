package updater

import (
	"context"
	"testing"

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
