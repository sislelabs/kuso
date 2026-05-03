// Notification configs (Discord webhook / generic webhook / Slack).
// Mirrors the web UI at /settings/notifications. The server stores the
// canonical state; this client is a thin pass-through with the
// {success, data} envelope unwrapped where it makes sense for the CLI.

package kusoApi

import "github.com/go-resty/resty/v2"

// NotificationBody is the create/update payload. Matches notifBody in
// server-go/internal/http/handlers/notifications.go.
type NotificationBody struct {
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	Type      string         `json:"type"` // "discord" | "webhook" | "slack"
	Pipelines []string       `json:"pipelines"`
	Events    []string       `json:"events"`
	Config    map[string]any `json:"config"`
}

func (k *KusoClient) ListNotifications() (*resty.Response, error) {
	return k.client.Get("/api/notifications")
}

func (k *KusoClient) GetNotification(id string) (*resty.Response, error) {
	return k.client.Get("/api/notifications/" + id)
}

func (k *KusoClient) CreateNotification(body NotificationBody) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Post("/api/notifications")
}

func (k *KusoClient) UpdateNotification(id string, body NotificationBody) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Put("/api/notifications/" + id)
}

func (k *KusoClient) DeleteNotification(id string) (*resty.Response, error) {
	return k.client.Delete("/api/notifications/" + id)
}

func (k *KusoClient) TestNotification(id string) (*resty.Response, error) {
	return k.client.Post("/api/notifications/" + id + "/test")
}
