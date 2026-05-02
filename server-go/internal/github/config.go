// Package github wraps the GitHub App + webhook integration.
//
// Two surfaces, one config:
//   - The webhook receiver verifies HMAC and dispatches event handlers
//     (push → trigger build, pull_request → preview env CRUD,
//     installation/installation_repositories → cache refresh).
//   - The App client mints installation tokens, lists installations +
//     repos, and resolves a branch ref to a commit SHA (used by the
//     builds package).
//
// Token caching is delegated to ghinstallation.NewKeyFromFile — DO NOT
// rebuild the transport per call (§6.3 landmine: rate limits exhaust
// inside an hour without caching).
package github

import (
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Config bundles the env-driven knobs the package needs. Construct via
// LoadConfig, which fails loud when GITHUB_APP_ID or GITHUB_APP_PRIVATE_KEY
// is missing — we'd rather refuse to boot the github surface than ship a
// silently-broken App client.
type Config struct {
	AppID         int64
	PrivateKey    []byte // PEM-encoded RSA private key
	WebhookSecret string
	// AppSlug is used to compute the public install URL. Mirrors the
	// "Public URL slug" field of the GitHub App settings.
	AppSlug string
}

// LoadConfig parses GITHUB_APP_ID, GITHUB_APP_PRIVATE_KEY,
// GITHUB_APP_WEBHOOK_SECRET, and GITHUB_APP_SLUG from the environment.
// Returns (nil, nil) when nothing is configured — callers treat that as
// "GitHub disabled".
func LoadConfig() (*Config, error) {
	appIDRaw := os.Getenv("GITHUB_APP_ID")
	priv := os.Getenv("GITHUB_APP_PRIVATE_KEY")
	if appIDRaw == "" && priv == "" {
		return nil, nil
	}
	if appIDRaw == "" || priv == "" {
		return nil, errors.New("github: both GITHUB_APP_ID and GITHUB_APP_PRIVATE_KEY must be set")
	}
	appID, err := strconv.ParseInt(strings.TrimSpace(appIDRaw), 10, 64)
	if err != nil {
		return nil, errors.New("github: GITHUB_APP_ID is not numeric")
	}
	// PRIVATE_KEY is sometimes shipped with literal "\n" sequences (e.g.
	// when stuffed into a single-line k8s Secret value). Unfold those to
	// real newlines so the PEM parses. Idempotent for already-folded
	// content.
	privBytes := []byte(strings.ReplaceAll(priv, `\n`, "\n"))
	return &Config{
		AppID:         appID,
		PrivateKey:    privBytes,
		WebhookSecret: os.Getenv("GITHUB_APP_WEBHOOK_SECRET"),
		AppSlug:       os.Getenv("GITHUB_APP_SLUG"),
	}, nil
}

// IsConfigured reports whether the package has enough config to talk to
// GitHub. The webhook handler still needs a separate WebhookSecret check.
func (c *Config) IsConfigured() bool {
	return c != nil && c.AppID > 0 && len(c.PrivateKey) > 0
}

// InstallURL is the public URL users hit to install the App.
func (c *Config) InstallURL() string {
	if c == nil || c.AppSlug == "" {
		return ""
	}
	return "https://github.com/apps/" + c.AppSlug + "/installations/new"
}

// HTTPClient returns a *http.Client suitable for App-level (not
// installation-level) calls. Callers usually go through Client.App(),
// which wraps this with the App JWT transport.
func defaultHTTPClient() *http.Client {
	return &http.Client{}
}
