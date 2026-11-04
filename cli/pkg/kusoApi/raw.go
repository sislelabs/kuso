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
	req := k.client.take()
	for key, val := range headers {
		req.SetHeader(key, val)
		// resty writes the stored token over any Authorization header,
		// so `-H 'Authorization: …'` was silently ignored. Drop the
		// stored token for this call when the caller supplies one.
		if strings.EqualFold(key, "Authorization") {
			req.Token = ""
		}
	}
	if body != nil {
		// The JSON Content-Type default comes from the base request; a
		// caller-supplied Content-Type header above wins.
		req.SetBody(body)
	}
	return req.Execute(strings.ToUpper(method), path)
}
