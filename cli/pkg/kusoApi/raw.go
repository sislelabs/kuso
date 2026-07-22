// raw.go — a gh-api-style passthrough. `kuso api <METHOD> <path>` uses
// this to hit any /api endpoint with the CLI's configured bearer token,
// so a new server endpoint is reachable before it gets a typed method.

package kusoApi

import (
	"strings"

	"github.com/go-resty/resty/v2"
)

// Raw executes an arbitrary request against the configured instance.
// method is a case-insensitive HTTP verb; path is used verbatim (the
// caller normalizes the leading slash / "/api" prefix). body nil ->
// no body; headers nil -> none. Returns the raw *resty.Response so the
// caller decides how to render it.
func (k *KusoClient) Raw(method, path string, body []byte, headers map[string]string) (*resty.Response, error) {
	req := k.client
	for key, val := range headers {
		req.SetHeader(key, val)
	}
	if body != nil {
		// Default content type for a JSON passthrough; a caller-supplied
		// Content-Type header above still wins (SetHeader ran first, but
		// SetBody won't override an explicit header).
		if _, ok := headers["Content-Type"]; !ok {
			req.SetHeader("Content-Type", "application/json")
		}
		req.SetBody(body)
	}
	return req.Execute(strings.ToUpper(method), path)
}
