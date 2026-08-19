package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/http/handlers"
)

// notifMask mirrors the unexported envMaskSentinel in package handlers.
// If the sentinel ever changes, the UI round-trip contract changes with
// it — these tests failing on a mismatch is the point.
const notifMask = "••••••••"

// Guards the credential-leak finding: GET/List on /api/notifications
// serialized db.Notification.Config wholesale — bot tokens, SMTP
// passwords, and secret-bearing webhook URLs went to the wire in
// plaintext. Read paths must mask; the write path must treat the
// sentinel as "keep the stored value" so a read-modify-write from the
// UI can't clobber a real credential with the mask.

func notifHandler(d *db.DB) *handlers.NotificationsHandler {
	return &handlers.NotificationsHandler{DB: d, Logger: slog.Default()}
}

// notifReq builds a request carrying the chi {id} URL param.
func notifReq(method, id string, body string) *http.Request {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, "/api/notifications/"+id, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, "/api/notifications/"+id, nil)
	}
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", id)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}

func decodeNotifData(t *testing.T, rr *httptest.ResponseRecorder) db.Notification {
	t.Helper()
	var envelope struct {
		Data db.Notification `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %q: %v", rr.Body.String(), err)
	}
	return envelope.Data
}

func createNotif(t *testing.T, h *handlers.NotificationsHandler, body string) db.Notification {
	t.Helper()
	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %q", rr.Code, rr.Body.String())
	}
	return decodeNotifData(t, rr)
}

func TestNotifications_ReadMasksCredentials(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)
	ctx := context.Background()

	created := createNotif(t, h, `{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"123456:real-bot-token","chatId":"-100200300"}}`)

	// Create response must already be masked — it echoes the config.
	if got := created.Config["botToken"]; got != notifMask {
		t.Errorf("create response botToken = %v, want mask", got)
	}
	if got := created.Config["chatId"]; got != "-100200300" {
		t.Errorf("create response chatId = %v, want plain value (not credential-shaped)", got)
	}

	// The DB row keeps the real credential.
	stored, err := d.FindNotification(ctx, created.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := stored.Config["botToken"]; got != "123456:real-bot-token" {
		t.Fatalf("stored botToken = %v, want the real value", got)
	}

	// Get masks.
	rr := httptest.NewRecorder()
	h.Get(rr, notifReq(http.MethodGet, created.ID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("get: %d %q", rr.Code, rr.Body.String())
	}
	if got := decodeNotifData(t, rr).Config["botToken"]; got != notifMask {
		t.Errorf("Get botToken = %v, want mask", got)
	}

	// List masks.
	rr = httptest.NewRecorder()
	h.List(rr, httptest.NewRequest(http.MethodGet, "/api/notifications", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list: %d %q", rr.Code, rr.Body.String())
	}
	var listEnvelope struct {
		Data []db.Notification `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &listEnvelope); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listEnvelope.Data) != 1 {
		t.Fatalf("list len = %d, want 1", len(listEnvelope.Data))
	}
	if got := listEnvelope.Data[0].Config["botToken"]; got != notifMask {
		t.Errorf("List botToken = %v, want mask", got)
	}
}

// The UI read-modify-write shape: GET (masked) → change one field →
// PUT the whole config back, sentinel included. The stored credential
// must survive; the changed field must land.
func TestNotifications_UpdateSentinelPreservesCredential(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)
	ctx := context.Background()

	created := createNotif(t, h, `{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"123456:real-bot-token","chatId":"-100200300"}}`)

	rr := httptest.NewRecorder()
	h.Update(rr, notifReq(http.MethodPut, created.ID, `{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"`+notifMask+`","chatId":"-100999999"}}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %q", rr.Code, rr.Body.String())
	}
	if got := decodeNotifData(t, rr).Config["botToken"]; got != notifMask {
		t.Errorf("update response botToken = %v, want mask", got)
	}

	stored, err := d.FindNotification(ctx, created.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := stored.Config["botToken"]; got != "123456:real-bot-token" {
		t.Errorf("stored botToken = %v — the sentinel clobbered the real credential", got)
	}
	if got := stored.Config["chatId"]; got != "-100999999" {
		t.Errorf("stored chatId = %v, want the updated value", got)
	}
}

// A real replacement value must still overwrite.
func TestNotifications_UpdateRealValueOverwrites(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)
	ctx := context.Background()

	created := createNotif(t, h, `{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"123456:old-token","chatId":"-1"}}`)

	rr := httptest.NewRecorder()
	h.Update(rr, notifReq(http.MethodPut, created.ID, `{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"654321:new-token","chatId":"-1"}}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("update: %d %q", rr.Code, rr.Body.String())
	}
	stored, err := d.FindNotification(ctx, created.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := stored.Config["botToken"]; got != "654321:new-token" {
		t.Errorf("stored botToken = %v, want the new value", got)
	}
}

// Slack/Discord/webhook URLs embed the secret in the path — they mask
// too, and the sentinel round-trip must not trip URL validation.
func TestNotifications_WebhookURLMaskedAndPreserved(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)
	ctx := context.Background()

	const realURL = "https://hooks.slack.com/services/T000/B000/supersecret"
	created := createNotif(t, h, `{"name":"sl","type":"slack","enabled":true,
		"config":{"url":"`+realURL+`"}}`)
	if got := created.Config["url"]; got != notifMask {
		t.Errorf("create response url = %v, want mask", got)
	}

	// Round-trip the mask: only the name changes.
	rr := httptest.NewRecorder()
	h.Update(rr, notifReq(http.MethodPut, created.ID, `{"name":"sl-renamed","type":"slack","enabled":true,
		"config":{"url":"`+notifMask+`"}}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("update with sentinel url: %d %q", rr.Code, rr.Body.String())
	}
	stored, err := d.FindNotification(ctx, created.ID)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got := stored.Config["url"]; got != realURL {
		t.Errorf("stored url = %v, want the real webhook URL preserved", got)
	}
	if stored.Name != "sl-renamed" {
		t.Errorf("name = %q, want the rename to land", stored.Name)
	}
}

// On create there is no stored value to resolve the sentinel against —
// storing it literally would make the mask the credential.
func TestNotifications_CreateRejectsSentinel(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)

	rr := httptest.NewRecorder()
	h.Create(rr, httptest.NewRequest(http.MethodPost, "/api/notifications", strings.NewReader(
		`{"name":"tg","type":"telegram","enabled":true,
		"config":{"botToken":"`+notifMask+`","chatId":"-1"}}`)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create with sentinel: %d, want 400 (%q)", rr.Code, rr.Body.String())
	}
}

// Email SMTP passwords mask; the non-credential coordinates stay
// readable so the settings page remains usable.
func TestNotifications_EmailPasswordMasked(t *testing.T) {
	d := openHandlerTestDB(t)
	h := notifHandler(d)

	created := createNotif(t, h, `{"name":"mail","type":"email","enabled":true,
		"config":{"host":"smtp.example.com","from":"a@b.c","to":"x@y.z","username":"mailer","password":"smtp-secret"}}`)
	if got := created.Config["password"]; got != notifMask {
		t.Errorf("password = %v, want mask", got)
	}
	if got := created.Config["host"]; got != "smtp.example.com" {
		t.Errorf("host = %v, want plain", got)
	}
	if got := created.Config["from"]; got != "a@b.c" {
		t.Errorf("from = %v, want plain", got)
	}
}
