// Log search + alert rule API client.

package kusoApi

import "github.com/go-resty/resty/v2"

// SearchLogs hits the FTS5-backed log search endpoint scoped to one
// service. Empty `q` returns every line (newest first) in the
// optional time range.
func (k *KusoClient) SearchLogs(project, service string, q map[string]string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/services/" + esc(service) + "/logs/search" + buildLogQuery(q))
}

func (k *KusoClient) SearchProjectLogs(project string, q map[string]string) (*resty.Response, error) {
	return k.client.Get("/api/projects/" + esc(project) + "/logs/search" + buildLogQuery(q))
}

func buildLogQuery(q map[string]string) string {
	parts := ""
	for key, val := range q {
		if val == "" {
			continue
		}
		sep := "?"
		if parts != "" {
			sep = "&"
		}
		parts += sep + key + "=" + urlEscape(val)
	}
	return parts
}

// urlEscape escapes a query-string value. Inline mini implementation
// to avoid pulling in net/url and ballooning the import graph for
// this one helper.
func urlEscape(s string) string {
	out := make([]byte, 0, len(s)*2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			out = append(out, c)
			continue
		}
		const hex = "0123456789ABCDEF"
		out = append(out, '%', hex[c>>4], hex[c&0xF])
	}
	return string(out)
}

// ---- Alert rules ----

type CreateAlertRequest struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Project         string   `json:"project,omitempty"`
	Service         string   `json:"service,omitempty"`
	Query           string   `json:"query,omitempty"`
	ThresholdInt    *int64   `json:"thresholdInt,omitempty"`
	ThresholdFloat  *float64 `json:"thresholdFloat,omitempty"`
	WindowSeconds   int      `json:"windowSeconds,omitempty"`
	Severity        string   `json:"severity,omitempty"`
	ThrottleSeconds int      `json:"throttleSeconds,omitempty"`
}

func (k *KusoClient) ListAlerts() (*resty.Response, error) {
	return k.client.Get("/api/alerts")
}

func (k *KusoClient) CreateAlert(req CreateAlertRequest) (*resty.Response, error) {
	k.client.SetBody(req)
	return k.client.Post("/api/alerts")
}

func (k *KusoClient) DeleteAlert(id string) (*resty.Response, error) {
	return k.client.Delete("/api/alerts/" + esc(id))
}

func (k *KusoClient) EnableAlert(id string) (*resty.Response, error) {
	return k.client.Post("/api/alerts/" + esc(id) + "/enable")
}

func (k *KusoClient) DisableAlert(id string) (*resty.Response, error) {
	return k.client.Post("/api/alerts/" + esc(id) + "/disable")
}
