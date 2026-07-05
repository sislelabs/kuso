// notifications_feed.go — client methods for the in-app notification
// feed (the bell-icon view): list recent events, the cheap unread
// counter, mark-all-read, and clear. All admin-gated server-side —
// the feed is the operator's instance-wide pager, not a per-user inbox.

package kusoApi

import (
	"net/url"
	"strconv"

	"github.com/go-resty/resty/v2"
)

// NotificationEvent is one feed row. Mirrors the read shape of
// server-go internal/db.NotificationEvent. ReadAt is nil when unread.
type NotificationEvent struct {
	ID        int64             `json:"id"`
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Body      string            `json:"body,omitempty"`
	Severity  string            `json:"severity,omitempty"`
	Project   string            `json:"project,omitempty"`
	Service   string            `json:"service,omitempty"`
	URL       string            `json:"url,omitempty"`
	Extra     map[string]string `json:"extra,omitempty"`
	CreatedAt string            `json:"createdAt"`
	ReadAt    *string           `json:"readAt,omitempty"`
}

// NotificationFeed returns the most recent feed events. limit <= 0
// leaves it to the server default (50). unread=true narrows to events
// that haven't been marked read. Response: a bare JSON array of
// NotificationEvent.
func (k *KusoClient) NotificationFeed(limit int, unread bool) (*resty.Response, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	if unread {
		q.Set("unread", "true")
	}
	path := "/api/notifications/feed"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return k.client.Get(path)
}

// NotificationFeedUnreadCount returns the unread-event count the bell
// badge polls. Response: {"unread": N}.
func (k *KusoClient) NotificationFeedUnreadCount() (*resty.Response, error) {
	return k.client.Get("/api/notifications/feed/unread-count")
}

// NotificationFeedReadAll stamps readAt on every unread event. 204 on
// success.
func (k *KusoClient) NotificationFeedReadAll() (*resty.Response, error) {
	return k.client.Post("/api/notifications/feed/read-all")
}

// NotificationFeedClear deletes every event in the in-app feed. 204 on
// success. Webhook fan-out for in-flight events is unaffected.
func (k *KusoClient) NotificationFeedClear() (*resty.Response, error) {
	return k.client.Delete("/api/notifications/feed")
}

// NotificationMyFeed returns recent feed events scoped to the caller's
// project memberships (admins see everything). Read-only — no unread /
// read-all / clear variants. Response: a bare JSON array of
// NotificationEvent (same shape as NotificationFeed).
func (k *KusoClient) NotificationMyFeed(limit int) (*resty.Response, error) {
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/api/notifications/my-feed"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	return k.client.Get(path)
}

// NotificationOutboxStats returns the webhook delivery queue health:
// {"pending": N, "dead": N}. A non-zero dead count means at least one
// external webhook channel is permanently misconfigured.
func (k *KusoClient) NotificationOutboxStats() (*resty.Response, error) {
	return k.client.Get("/api/notifications/outbox-stats")
}
