// Log drains: forward app logs to an HTTP JSON endpoint, an OTLP/HTTP
// collector or Loki. Mirrors /api/drains in server-go.

package kusoApi

import "github.com/go-resty/resty/v2"

// DrainBody is the create payload. Matches drainBody in
// server-go/internal/http/handlers/drains.go.
type DrainBody struct {
	Name    string            `json:"name,omitempty"`
	Type    string            `json:"type"` // "http" | "otlp" | "loki"
	URL     string            `json:"url"`
	Project string            `json:"project,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Secret  string            `json:"secret,omitempty"`
	Enabled *bool             `json:"enabled,omitempty"`
}

func (k *KusoClient) ListDrains() (*resty.Response, error) {
	return k.client.Get("/api/drains")
}

func (k *KusoClient) CreateDrain(body DrainBody) (*resty.Response, error) {
	k.client.SetBody(body)
	return k.client.Post("/api/drains")
}

func (k *KusoClient) DeleteDrain(id string) (*resty.Response, error) {
	return k.client.Delete("/api/drains/" + esc(id))
}

func (k *KusoClient) TestDrain(id string) (*resty.Response, error) {
	return k.client.Post("/api/drains/" + esc(id) + "/test")
}
