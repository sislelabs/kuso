// github_manifest.go — one-click GitHub App creation via the GitHub App
// Manifest flow. This replaces the error-prone manual path (open
// github.com/settings/apps/new, hand-fill an 8-field form, download a
// .pem, paste 7 values back) with a single button:
//
//  1. GET  /api/github/manifest → returns the fully-prefilled manifest
//     JSON + the GitHub URL to POST it to. The browser auto-submits a
//     form to github.com/settings/apps/new?state=… with the manifest;
//     the user just clicks "Create GitHub App".
//  2. GitHub creates the App from the manifest (name, URLs, permissions,
//     webhook events — all authored by kuso, so nothing can be typo'd or
//     forgotten) and redirects back to
//     GET /api/github/manifest-callback?code=… .
//  3. We exchange the temporary code via
//     POST https://api.github.com/app-manifests/{code}/conversions,
//     which returns the App id, slug, BOTH secrets, the webhook secret,
//     AND the private key — everything, no copy-paste. We seed the
//     kuso-github-app Secret (reusing Configure's seedAndRestart) and
//     bounce the user to the install step.
//
// Base URL: derived from a trusted source via publicBaseURL —
// KUSO_PUBLIC_URL when set, else X-Forwarded-Host only from a peer in
// KUSO_TRUSTED_PROXIES, else r.Host. We deliberately do NOT trust a raw
// X-Forwarded-Host from an arbitrary peer: it would let an attacker spoof
// the Host and point the created App's webhook/callback URLs at their own
// domain.
package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// manifestStore is a short-lived nonce set: the CSRF guard for the
// App-Manifest round-trip. A state issued by the authed ManifestConfig
// must be consumable by the public ManifestCallback — and those mount on
// two separate handler instances (different router functions) — so the
// store is a PACKAGE-LEVEL singleton (globalManifestStore) rather than
// per-handler. The flow completes in seconds and a lost entry just means
// "start over", so no persistence needed. Mutex-guarded since the two
// routes can be hit concurrently.
type manifestStore struct {
	mu     sync.Mutex
	states map[string]time.Time
}

func newManifestStore() *manifestStore { return &manifestStore{states: map[string]time.Time{}} }

// globalManifestStore is shared across every GithubConfigureHandler so
// the issue/consume pair works across the public + authed instances.
var globalManifestStore = newManifestStore()

func (m *manifestStore) issue() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	s := hex.EncodeToString(b)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Opportunistic GC of expired states on each issue (bounded set).
	cutoff := time.Now().Add(-10 * time.Minute)
	for k, t := range m.states {
		if t.Before(cutoff) {
			delete(m.states, k)
		}
	}
	m.states[s] = time.Now()
	return s
}

func (m *manifestStore) consume(s string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.states[s]
	if !ok || time.Since(t) > 10*time.Minute {
		delete(m.states, s)
		return false
	}
	delete(m.states, s)
	return true
}

// githubAppManifest is the manifest schema GitHub accepts at
// /settings/apps/new. Only the fields kuso cares about are modeled.
type githubAppManifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     map[string]string `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url"`
	CallbackURLs       []string          `json:"callback_urls"`
	SetupURL           string            `json:"setup_url"`
	Public             bool              `json:"public"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// ManifestConfig returns the prefilled manifest + the GitHub POST target
// for the given request's origin. GET /api/github/manifest.
//
// Query param `org` (optional) targets an org's app-creation page
// instead of the personal one.
func (h *GithubConfigureHandler) ManifestConfig(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	// Derive the manifest's URLs from a TRUSTED source, not raw request
	// headers. publicBaseURL prefers KUSO_PUBLIC_URL (operator's source of
	// truth) and only honors X-Forwarded-Host from peers in
	// KUSO_TRUSTED_PROXIES — otherwise it falls back to r.Host. This
	// closes the Host-spoofing vector: a spoofed X-Forwarded-Host from an
	// untrusted peer can no longer point the created App's webhook /
	// callback / redirect URLs at an attacker domain.
	base := publicBaseURL(r)
	// App name must be globally unique on GitHub; derive from the host's
	// first label + a short suffix so retries after a name clash differ.
	host := strings.TrimPrefix(base, "https://")
	host = strings.TrimPrefix(host, "http://")
	label := host
	if i := strings.IndexByte(label, '.'); i > 0 {
		label = label[:i]
	}
	name := "kuso-" + label

	manifest := githubAppManifest{
		Name:           name,
		URL:            base + "/",
		HookAttributes: map[string]string{"url": base + "/api/github/webhook"},
		// redirect_url is where GitHub sends the temporary code after the
		// user creates the App. setup_url is where it sends them after
		// they INSTALL it (existing SetupCallback). callback_urls is the
		// OAuth login callback.
		RedirectURL:  base + "/api/github/manifest-callback",
		CallbackURLs: []string{base + "/api/auth/github/callback"},
		SetupURL:     base + "/api/github/setup-callback",
		Public:       false,
		// Match what the manual wizard asked for so behaviour is identical.
		DefaultEvents: []string{"push", "pull_request", "installation"},
		DefaultPermissions: map[string]string{
			"contents":      "write",
			"metadata":      "read",
			"pull_requests": "write",
			// The App manages its own webhook; no explicit webhook perm key
			// is needed in the manifest (GitHub wires the hook from
			// hook_attributes). Deployments read/write lets kuso post
			// deployment statuses back to the PR.
			"deployments": "write",
		},
	}

	state := h.manifests().issue()
	postURL := "https://github.com/settings/apps/new"
	if org := strings.TrimSpace(r.URL.Query().Get("org")); org != "" {
		postURL = "https://github.com/organizations/" + org + "/settings/apps/new"
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"manifest": manifest,
		"postURL":  postURL,
		"state":    state,
	})
}

// conversionResponse is the subset of GitHub's app-manifest conversion
// response we consume. GitHub returns the whole App object + the secrets.
type conversionResponse struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	Owner         struct {
		Login string `json:"login"`
	} `json:"owner"`
}

// ManifestCallback handles GitHub's redirect after the user creates the
// App from the manifest. GET /api/github/manifest-callback?code=&state=.
// Exchanges the code for the full credential set and seeds the secret.
func (h *GithubConfigureHandler) ManifestCallback(w http.ResponseWriter, r *http.Request) {
	// This is a top-level browser redirect from GitHub, not an XHR — we
	// can't require a bearer here. The CSRF `state` we issued (admin-only)
	// is the gate: only someone who hit ManifestConfig as an admin has a
	// valid state, and it's single-use + short-TTL.
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" {
		http.Redirect(w, r, "/settings/github?error=missing_code", http.StatusFound)
		return
	}
	if state == "" || !h.manifests().consume(state) {
		http.Redirect(w, r, "/settings/github?error=bad_state", http.StatusFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	conv, err := exchangeManifestCode(ctx, code)
	if err != nil {
		h.Logger.Warn("github manifest: code exchange failed", "err", err)
		http.Redirect(w, r, "/settings/github?error=exchange_failed", http.StatusFound)
		return
	}

	pem := conv.PEM
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}
	data := map[string][]byte{
		"GITHUB_APP_ID":             []byte(strconv.FormatInt(conv.ID, 10)),
		"GITHUB_APP_SLUG":           []byte(conv.Slug),
		"GITHUB_APP_CLIENT_ID":      []byte(conv.ClientID),
		"GITHUB_APP_CLIENT_SECRET":  []byte(conv.ClientSecret),
		"GITHUB_APP_WEBHOOK_SECRET": []byte(conv.WebhookSecret),
		"GITHUB_APP_PRIVATE_KEY":    []byte(pem),
	}
	if conv.Owner.Login != "" {
		data["GITHUB_APP_ORG"] = []byte(conv.Owner.Login)
	}

	if err := h.seedAndRestart(ctx, data); err != nil {
		h.Logger.Warn("github manifest: seed failed", "err", err)
		http.Redirect(w, r, "/settings/github?error=seed_failed", http.StatusFound)
		return
	}

	h.Logger.Info("github app created via manifest", "slug", conv.Slug, "app_id", conv.ID, "owner", conv.Owner.Login)
	// The App now exists + kuso is restarting to load it. Bounce the user
	// to the settings page in a "just created, now install it" state — the
	// page polls setup-status + shows the install button once kuso is back.
	http.Redirect(w, r, "/settings/github?created="+conv.Slug, http.StatusFound)
}

// exchangeManifestCode calls GitHub's manifest conversion endpoint.
func exchangeManifestCode(ctx context.Context, code string) (*conversionResponse, error) {
	url := "https://api.github.com/app-manifests/" + code + "/conversions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("github conversion returned %d: %s", resp.StatusCode, string(body))
	}
	var conv conversionResponse
	if err := json.Unmarshal(body, &conv); err != nil {
		return nil, fmt.Errorf("decode conversion: %w", err)
	}
	if conv.ID == 0 || conv.PEM == "" || conv.WebhookSecret == "" {
		return nil, fmt.Errorf("conversion response missing required fields (id/pem/webhook_secret)")
	}
	return &conv, nil
}
