// Package coolify is a strictly read-only client for Coolify v4's
// REST API. Imported by both `kuso migrate coolify` (in cli/) and
// the /api/import/coolify/preview endpoint (in server-go/) so the
// classifier verdicts and kuso-shape mapping they emit can't drift.
//
// The client has a HARDCODED method allow-list (GET only) so a
// future bug or feature can't accidentally call DELETE / PATCH /
// POST against the source Coolify — every write happens on kuso,
// not on the user's source-of-truth instance.
package coolify

import (
	"context"
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

// maxResponseBytes caps how much of a Coolify API response we'll
// read into memory. A Coolify instance (or a DNS-spoofed server)
// returning a 2 GB body would OOM the kuso-server pod via the
// /api/import/coolify/preview endpoint. 32 MiB is generous for any
// realistic inventory (thousands of apps fit comfortably).
const maxResponseBytes = 32 << 20

// getRaw fetches a path under /api/v1/. Threads ctx so the caller's
// timeout actually cancels the underlying HTTP request — the
// previous version constructed a plain http.NewRequest and the
// import handler's 60s timeout was dead code. Non-2xx is surfaced
// as an error including the status code + first 256 bytes of
// response. Response body is capped via io.LimitReader (B5).
func (c *Client) getRaw(ctx context.Context, path string) ([]byte, error) {
	url := c.baseURL + "/api/v1" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("coolify GET %s: read body: %w", path, err)
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, fmt.Errorf("coolify GET %s: response exceeds %d bytes — refusing to buffer", path, maxResponseBytes)
	}
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
func get[T any](ctx context.Context, c *Client, path string) (T, error) {
	var zero T
	body, err := c.getRaw(ctx, path)
	if err != nil {
		return zero, err
	}
	var out T
	if err := json.Unmarshal(body, &out); err != nil {
		return zero, fmt.Errorf("decode %s: %w", path, err)
	}
	return out, nil
}
