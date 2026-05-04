package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

// NotificationsHandler handles /api/notifications full CRUD.
//
// Wire shape mirrors the TS controller:
//
//	{ success: true, data: ..., message?: string }
//
// We keep that envelope so the Vue store doesn't need a remap.
// notifySink is the minimal interface the handler needs from the
// notify dispatcher (avoids importing the full type into router/Deps).
type notifySink interface {
	EmitEnvelope(notify.EmitEnvelope)
	SendDirect(ctx context.Context, n *db.Notification, e notify.Event) error
}

type NotificationsHandler struct {
	Notify notifySink

	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *NotificationsHandler) Mount(r chi.Router) {
	r.Get("/api/notifications", h.List)
	r.Get("/api/notifications/{id}", h.Get)
	r.Post("/api/notifications", h.Create)
	r.Put("/api/notifications/{id}", h.Update)
	r.Delete("/api/notifications/{id}", h.Delete)
	r.Post("/api/notifications/{id}/test", h.Test)
	// In-app feed — every dispatched event lands in NotificationEvent
	// regardless of sink config, so the bell icon always has data
	// even when no webhooks are wired. Limit + unread badge live here.
	r.Get("/api/notifications/feed", h.Feed)
	r.Get("/api/notifications/feed/unread-count", h.FeedUnread)
	r.Post("/api/notifications/feed/read-all", h.FeedReadAll)
}

// Feed returns the most recent notification events. ?limit=N (clamp
// to 200) and ?unread=true narrow the result.
func (h *NotificationsHandler) Feed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	unread := r.URL.Query().Get("unread") == "true"
	out, err := h.DB.ListNotificationEvents(ctx, limit, unread)
	if err != nil {
		h.fail(w, "feed", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// FeedUnread is the cheap counter the bell badge polls.
func (h *NotificationsHandler) FeedUnread(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	n, err := h.DB.CountUnreadNotificationEvents(ctx)
	if err != nil {
		h.fail(w, "unread count", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
}

// FeedReadAll stamps readAt on every unread event. Called when the
// user opens the bell popover.
func (h *NotificationsHandler) FeedReadAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.MarkAllNotificationEventsRead(ctx); err != nil {
		h.fail(w, "mark read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Test sends a synthetic event to the chosen notification config so
// the user can verify their Discord webhook URL works without having
// to wait for a real build to fire. Read the config, push one
// EventEnvelope onto the notify dispatcher, return 204.
func (h *NotificationsHandler) Test(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	n, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	if h.Notify == nil {
		http.Error(w, "notify dispatcher not wired", http.StatusServiceUnavailable)
		return
	}
	// Test sends bypass the event-whitelist filter — otherwise a
	// notification that doesn't have `test.ping` in its events list
	// (i.e. every real-world config) would silently drop the test.
	// SendDirect targets ONE notification, ignoring filters.
	if err := h.Notify.SendDirect(ctx, n, notify.Event{
		Type:     "test.ping",
		Title:    fmt.Sprintf("Test from kuso (%s)", n.Name),
		Body:     "If you can read this, your notification channel is wired up correctly.",
		Severity: "info",
	}); err != nil {
		h.Logger.Error("notify test", "name", n.Name, "err", err)
		http.Error(w, "test send failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func notifCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *NotificationsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	out, err := h.DB.ListNotifications(ctx)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	if out == nil {
		out = []db.Notification{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": out})
}

func (h *NotificationsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	out, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": out})
}

type notifBody struct {
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	Type      string         `json:"type"`
	Pipelines []string       `json:"pipelines"`
	Events    []string       `json:"events"`
	Config    map[string]any `json:"config"`
}

func (h *NotificationsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body notifBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.Type == "" {
		http.Error(w, "name and type required", http.StatusBadRequest)
		return
	}
	if !validNotificationType(body.Type) {
		http.Error(w, "type must be slack, webhook, or discord", http.StatusBadRequest)
		return
	}
	if err := validateNotificationConfig(body.Type, body.Config); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n := &db.Notification{
		ID: randomID(), Name: body.Name, Enabled: body.Enabled, Type: body.Type,
		Pipelines: body.Pipelines, Events: body.Events, Config: body.Config,
	}
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.CreateNotification(ctx, n); err != nil {
		h.fail(w, "create", err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "data": n, "message": "Notification created successfully"})
}

func (h *NotificationsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var body notifBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Type != "" && !validNotificationType(body.Type) {
		http.Error(w, "type must be slack, webhook, or discord", http.StatusBadRequest)
		return
	}
	if body.Type != "" && body.Config != nil {
		if err := validateNotificationConfig(body.Type, body.Config); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := notifCtx(r)
	defer cancel()
	existing, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	// Apply partial: only overwrite supplied fields.
	if body.Name != "" {
		existing.Name = body.Name
	}
	existing.Enabled = body.Enabled
	if body.Type != "" {
		existing.Type = body.Type
	}
	if body.Pipelines != nil {
		existing.Pipelines = body.Pipelines
	}
	if body.Events != nil {
		existing.Events = body.Events
	}
	if body.Config != nil {
		existing.Config = body.Config
	}
	if err := h.DB.UpdateNotification(ctx, existing); err != nil {
		h.fail(w, "update", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": existing, "message": "Notification updated successfully"})
}

func (h *NotificationsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.DeleteNotification(ctx, chi.URLParam(r, "id")); err != nil {
		h.fail(w, "delete", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Notification deleted successfully"})
}

func (h *NotificationsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	default:
		h.Logger.Error("notifications handler", "op", op, "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
	}
}

func validNotificationType(t string) bool {
	switch t {
	case "slack", "webhook", "discord":
		return true
	}
	return false
}

func validateNotificationConfig(typ string, cfg map[string]any) error {
	if cfg == nil {
		return errors.New("config required")
	}
	url, _ := cfg["url"].(string)
	if url == "" {
		return errors.New("config.url required")
	}
	if typ == "slack" {
		if ch, _ := cfg["channel"].(string); ch == "" {
			return errors.New("slack notifications require config.channel")
		}
	}
	return nil
}
