// Package config holds runtime configuration for kuso-mcp.
package config

import (
	"errors"
	"os"
	"strings"
)

// Config is the runtime configuration for kuso-mcp.
type Config struct {
	// URL is the base URL of the kuso server (e.g. https://kuso.example.com).
	URL string

	// Token is the API token used to authenticate against the kuso server.
	Token string

	// ReadOnly disables tools that mutate state when true.
	ReadOnly bool
}

// FromEnv reads KUSO_URL and KUSO_TOKEN from the environment and returns a
// populated Config. ReadOnly defaults to false; callers set it from CLI flags.
func FromEnv() (*Config, error) {
	url := strings.TrimRight(strings.TrimSpace(os.Getenv("KUSO_URL")), "/")
	token := strings.TrimSpace(os.Getenv("KUSO_TOKEN"))

	if url == "" {
		return nil, errors.New("KUSO_URL is not set")
	}
	if token == "" {
		return nil, errors.New("KUSO_TOKEN is not set")
	}
	return &Config{URL: url, Token: token}, nil
}
