// Package coolify is a strictly read-only client for Coolify v4's
// REST API. Used by the `kuso migrate coolify` subcommand to inspect
// a remote Coolify instance and translate its resources into kuso
// CRs. The client has a HARDCODED method allow-list (GET only) so a
// future bug or feature can't accidentally call DELETE / PATCH /
// POST against the source Coolify — every write happens on kuso, not
// on the user's source-of-truth instance.
package coolify

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client wraps a base URL + bearer token. Construct with New + use
// Get to fetch a typed resource.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// New constructs a Client. baseURL is e.g. "https://ops.sisle.org"
// (no trailing slash, no /api/v1 path — we add that). Token is the
// "Bearer …" value.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

// guard refuses anything but GET. Defence-in-depth: every callsite
// goes through Get/getRaw which already only build GET requests, but
// this lives at the http.Request layer so a future copy-paste can't
// silently introduce a write call. The user-supplied token has full
// read+write scope; we don't want our tooling to be the thing that
// accidentally tears down their production resources.
func (c *Client) guard(req *http.Request) error {
	if req.Method != http.MethodGet {
		return fmt.Errorf("coolify client refuses non-GET (%s) — this tool is read-only by design", req.Method)
	}
	return nil
}

// getRaw fetches a path under /api/v1/. Returns the raw body so
// callers that want forward-compatibility (untyped fields) can
// decode partially. Non-2xx is surfaced as an error including the
// status code + first 256 bytes of response so debugging doesn't
// require a separate curl.
func (c *Client) getRaw(path string) ([]byte, error) {
	url := c.baseURL + "/api/v1" + path
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if err := c.guard(req); err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("coolify GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		head := body
		if len(head) > 256 {
			head = head[:256]
		}
		return nil, fmt.Errorf("coolify GET %s: %d %s — %s", path, resp.StatusCode, resp.Status, strings.TrimSpace(string(head)))
	}
	return body, nil
}

// get is the typed variant — decodes into the supplied destination.
func get[T any](c *Client, path string) (T, error) {
	var zero T
	body, err := c.getRaw(path)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, nil
}
