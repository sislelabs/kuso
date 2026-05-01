// Package kusoclient is a thin HTTP client for the kuso server REST API.
//
// It is intentionally minimal: the MCP layer above it does the shaping into
// intent-grouped tools, so this client just authenticates, parses JSON,
// and returns Go types.
package kusoclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/sislelabs/kuso/mcp/internal/config"
)

// Client wraps an http.Client and the kuso server URL + token.
type Client struct {
	cfg  *config.Config
	http *http.Client
}

// New returns a Client backed by an http.Client with a sensible timeout.
func New(cfg *config.Config) *Client {
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// URL returns the base URL of the configured kuso server.
func (c *Client) URL() string { return c.cfg.URL }

// ReadOnly reports whether mutating endpoints should be refused.
func (c *Client) ReadOnly() bool { return c.cfg.ReadOnly }

// APIError is returned when the kuso server replies with a non-2xx status.
type APIError struct {
	Status int
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kuso server %s returned %d: %s", e.Path, e.Status, truncate(e.Body, 200))
}

// GetJSON issues a GET against the kuso server and decodes the JSON response
// into out. If out is nil, the response body is discarded. Path must begin
// with a slash (e.g. "/api/apps").
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}

// PostJSON issues a POST with a JSON body and decodes the JSON response.
func (c *Client) PostJSON(ctx context.Context, path string, body, out any) error {
	if c.cfg.ReadOnly {
		return fmt.Errorf("kuso-mcp is in read-only mode; refusing %s %s", http.MethodPost, path)
	}
	return c.doJSON(ctx, http.MethodPost, path, body, out)
}

// DeleteJSON issues a DELETE. Refused in read-only mode.
func (c *Client) DeleteJSON(ctx context.Context, path string) error {
	if c.cfg.ReadOnly {
		return fmt.Errorf("kuso-mcp is in read-only mode; refusing %s %s", http.MethodDelete, path)
	}
	return c.doJSON(ctx, http.MethodDelete, path, nil, nil)
}

func (c *Client) doJSON(ctx context.Context, method, path string, body, out any) error {
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
		rdr = strings.NewReader(string(buf))
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.URL+path, rdr)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kuso server request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Path: path, Body: string(respBody)}
	}
	if out == nil || len(respBody) == 0 {
		return nil
	}
	if err := json.Unmarshal(respBody, out); err != nil {
		return fmt.Errorf("decode %s response: %w (body: %s)", path, err, truncate(string(respBody), 200))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
