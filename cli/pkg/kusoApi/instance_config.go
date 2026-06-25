// Instance-config API client. Mirrors GET/POST /api/config — the Kuso
// CR spec ("settings"). GET is readable by any authenticated user; POST
// requires settings:admin (AdminOnly middleware) and replaces the whole
// settings object.
package kusoApi

import "github.com/go-resty/resty/v2"

// GetInstanceConfig reads the cached Kuso CR spec. GET /api/config. The
// body is {settings:{...}, secrets:{...}}.
func (k *KusoClient) GetInstanceConfig() (*resty.Response, error) {
	return k.client.Get("/api/config")
}

// SetInstanceConfig replaces the Kuso CR spec. POST /api/config. The
// body is {settings:{...}} — the entire settings object is replaced, so
// callers should read-modify-write to preserve untouched keys.
func (k *KusoClient) SetInstanceConfig(settings map[string]any) (*resty.Response, error) {
	k.client.SetBody(map[string]any{"settings": settings})
	return k.client.Post("/api/config")
}
