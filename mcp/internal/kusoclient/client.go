// Package kusoclient is a thin HTTP client for the kuso server REST API.
//
// It is intentionally minimal in v0.1: the MCP layer above it does the
// shaping into intent-grouped tools, so this client just authenticates,
// parses JSON, and returns Go types.
package kusoclient

import (
	"net/http"
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
