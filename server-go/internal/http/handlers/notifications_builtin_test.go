package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

type fakeNotifySink struct {
	got     []notify.Event
	results []notify.DeliveryResult
	muted   bool
}

func (f *fakeNotifySink) EmitEnvelope(notify.EmitEnvelope) {}
func (f *fakeNotifySink) SendDirect(context.Context, *db.Notification, notify.Event) error {
	return nil
}
func (f *fakeNotifySink) InvalidateNotifications() {}
func (f *fakeNotifySink) DeliverNow(_ context.Context, e notify.Event) ([]notify.DeliveryResult, bool, error) {
	f.got = append(f.got, e)
	return f.results, f.muted, nil
}

func postEventTypeTest(t *testing.T, h *NotificationsHandler, typ, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := chi.NewRouter()
	h.Mount(r)
	ctx := auth.WithClaimsForTest(context.Background(), &auth.Claims{UserID: "u1", Permissions: []string{string(auth.PermSettingsAdmin)}})
	req := httptest.NewRequest(http.MethodPost, "/api/notifications/event-types/"+typ+"/test", strings.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestTestEventType_DispatchesSyntheticEventOfThatType(t *testing.T) {
	sink := &fakeNotifySink{
		results: []notify.DeliveryResult{{Channel: "c1", Name: "ops", Type: "discord", OK: false, Error: "DNS lookup failed"}},
		muted:   true,
	}
	rec := postEventTypeTest(t, &NotificationsHandler{Notify: sink}, "build.failed", `{"project":"shop"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if len(sink.got) != 1 {
		t.Fatalf("DeliverNow called %d times, want 1", len(sink.got))
	}
	e := sink.got[0]
	if e.Type != notify.EventBuildFailed || e.Project != "shop" || e.Severity != "error" || e.Extra["test"] != "true" {
		t.Errorf("event = %+v, want build.failed for shop at error severity marked test", e)
	}
	if !strings.HasPrefix(e.Title, "Test: ") {
		t.Errorf("title %q should say it's a test", e.Title)
	}
	var resp alertTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Results) != 1 || resp.Results[0].Error != "DNS lookup failed" || !resp.Muted {
		t.Errorf("response = %+v, want the sink's results and muted", resp)
	}
}

func TestTestEventType_RejectsUnknownAndUnsubscribableTypes(t *testing.T) {
	for _, typ := range []string{"nope.event", string(notify.EventTestPing)} {
		sink := &fakeNotifySink{}
		rec := postEventTypeTest(t, &NotificationsHandler{Notify: sink}, typ, "")
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", typ, rec.Code)
		}
		if len(sink.got) != 0 {
			t.Errorf("%s: dispatched %d events, want none", typ, len(sink.got))
		}
	}
}

func TestLatestDeliveries_StatusAndSafeErrors(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	delivered := t0.Add(time.Minute)
	channels := []db.Notification{{ID: "c1", Name: "ops", Type: "discord"}}
	rows := []db.OutboxLatest{
		{EventType: "build.failed", NotificationID: "c1", Attempts: 1, DeliveredAt: &delivered, CreatedAt: t0},
		{EventType: "pod.crashed", NotificationID: "c1", Attempts: db.MaxOutboxAttempts, CreatedAt: t0,
			LastError: `Post "https://discord.com/api/webhooks/123/SECRET": dial tcp: lookup discord.com: no such host`},
		{EventType: "uptime.down", NotificationID: "c1", Attempts: 2, CreatedAt: t0, LastError: "upstream 500"},
		{EventType: "backup.ok", NotificationID: "c1", CreatedAt: t0},
		{EventType: "alert.fired", RuleID: "r1", NotificationID: "gone", CreatedAt: t0},
	}
	got := latestDeliveries(rows, channels)
	want := map[string][2]string{
		"build.failed": {"delivered", ""},
		"pod.crashed":  {"failed", "DNS lookup failed"},
		"uptime.down":  {"retrying", "receiver returned HTTP 500"},
		"backup.ok":    {"pending", ""},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d (deleted channel dropped): %+v", len(got), len(want), got)
	}
	for _, d := range got {
		w := want[d.EventType]
		if d.Status != w[0] || d.Error != w[1] {
			t.Errorf("%s: status %q error %q, want %q %q", d.EventType, d.Status, d.Error, w[0], w[1])
		}
		if strings.Contains(d.Error, "SECRET") {
			t.Errorf("%s: error leaks the webhook URL: %q", d.EventType, d.Error)
		}
		if d.ChannelName != "ops" {
			t.Errorf("%s: channel name %q", d.EventType, d.ChannelName)
		}
	}
	if !got[0].At.Equal(delivered) {
		t.Errorf("delivered row At = %v, want deliveredAt %v", got[0].At, delivered)
	}
}
