package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

// Built-in alerts are the notify events kuso emits on its own (build
// failed, pod crashed, service down…). They have no rule row: a channel's
// event list is their routing. These endpoints let the Alerts page test
// one and show where it last went.

type eventTypeTestBody struct {
	// Project, when set, tests the routing a real event from that project
	// would get (project-filtered channels, mutes).
	Project string `json:"project"`
}

// TestEventType sends a synthetic event of one built-in type, marked as a
// test, to every channel a real one would reach, synchronously, and
// returns each delivery result. Nothing is written to the bell feed or
// the outbox.
func (h *NotificationsHandler) TestEventType(w http.ResponseWriter, r *http.Request) {
	typ := chi.URLParam(r, "type")
	info, ok := catalogueEntry(typ)
	if !ok {
		writeErr(w, http.StatusBadRequest, "unknown event type "+typ)
		return
	}
	var body eventTypeTestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if h.Notify == nil {
		writeErr(w, http.StatusServiceUnavailable, "notifications aren't configured on this server")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	results, muted, err := h.Notify.DeliverNow(ctx, builtinTestEvent(info, strings.TrimSpace(body.Project)))
	if err != nil {
		h.fail(w, "test event type", err)
		return
	}
	writeJSON(w, http.StatusOK, alertTestResponse{Results: results, Muted: muted})
}

func catalogueEntry(typ string) (notify.EventTypeInfo, bool) {
	for _, c := range notify.EventCatalogue {
		if string(c.Type) == typ {
			return c, true
		}
	}
	return notify.EventTypeInfo{}, false
}

// builtinTestEvent carries the type's usual severity so a channel's
// per-severity mention rules render as they would for the real thing.
func builtinTestEvent(info notify.EventTypeInfo, project string) notify.Event {
	sev := "info"
	if info.DefaultMention != "" {
		sev = "error"
	}
	return notify.Event{
		Type:      info.Type,
		Timestamp: time.Now().UTC(),
		Project:   project,
		Title:     "Test: " + info.Label,
		Body:      "Test notification for the built-in \"" + info.Label + "\" alert, sent from the kuso dashboard. Nothing is wrong.",
		Severity:  sev,
		Extra:     map[string]string{"test": "true"},
	}
}

// LatestDelivery is the newest delivery of one event type (or one alert
// rule, for alert.fired) to one channel.
type LatestDelivery struct {
	EventType   string `json:"eventType"`
	RuleID      string `json:"ruleId,omitempty"`
	Channel     string `json:"channel"`
	ChannelName string `json:"channelName"`
	ChannelType string `json:"channelType"`
	// Status: delivered | pending | retrying | failed (gave up after the retry cap).
	Status string    `json:"status"`
	Error  string    `json:"error,omitempty"`
	At     time.Time `json:"at"`
}

// LatestDeliveries reads the newest outbox row per event type, alert rule
// and channel. Errors are reduced to SafeDeliveryError categories because
// the stored text can carry the webhook URL. Rows for deleted channels
// are dropped.
func (h *NotificationsHandler) LatestDeliveries(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	rows, err := h.DB.LatestOutboxDeliveries(ctx)
	if err != nil {
		h.fail(w, "latest deliveries", err)
		return
	}
	channels, err := h.DB.ListNotifications(ctx)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	writeJSON(w, http.StatusOK, latestDeliveries(rows, channels))
}

func latestDeliveries(rows []db.OutboxLatest, channels []db.Notification) []LatestDelivery {
	byID := make(map[string]db.Notification, len(channels))
	for _, c := range channels {
		byID[c.ID] = c
	}
	out := []LatestDelivery{}
	for _, row := range rows {
		ch, ok := byID[row.NotificationID]
		if !ok {
			continue
		}
		d := LatestDelivery{
			EventType:   row.EventType,
			RuleID:      row.RuleID,
			Channel:     ch.ID,
			ChannelName: ch.Name,
			ChannelType: ch.Type,
			At:          row.CreatedAt,
		}
		switch {
		case row.DeliveredAt != nil:
			d.Status, d.At = "delivered", *row.DeliveredAt
		case row.Attempts >= db.MaxOutboxAttempts:
			d.Status = "failed"
		case row.Attempts == 0:
			d.Status = "pending"
		default:
			d.Status = "retrying"
		}
		if d.Status != "delivered" && row.LastError != "" {
			d.Error = notify.SafeDeliveryError(errors.New(row.LastError))
		}
		out = append(out, d)
	}
	return out
}
