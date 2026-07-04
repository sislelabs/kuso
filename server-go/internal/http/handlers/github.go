package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/github"
)

// GithubHandler exposes the webhook receiver and the admin/install
// routes. Cfg, Client, Cache, and Dispatcher are all optional — when
// the App isn't configured the routes either return a typed
// "configured: false" or 503.
type GithubHandler struct {
	Cfg        *github.Config
	Client     *github.Client
	Cache      github.CacheStore
	Dispatcher *github.Dispatcher
	Logger     *slog.Logger
	// DB is used for X-GitHub-Delivery replay protection — records
	// the delivery id on first sight; subsequent receipts of the
	// same id 200-no-op. Optional; when nil the dedup is skipped
	// (signature verification still applies).
	DB *db.DB
	// BaseCtx is the server's lifecycle context. Webhook dispatches
	// derive from this so a graceful shutdown cancels in-flight
	// preview-env creates / build triggers instead of leaving them
	// running against a closing kube client.
	BaseCtx context.Context

	// installationLimiter token-buckets webhook accepts per
	// installation id so a leaked secret can't trigger unbounded
	// preview-env spam. Lazy-init on first webhook.
	limiterMu       sync.Mutex
	installLimiters map[int64]*ghTokenBucket
}

// ghTokenBucket is a tiny per-installation token bucket. 60 tokens,
// refilled 1/sec → 60 webhooks per minute steady-state, 60 burst.
// GitHub's normal cadence (push, PR open, PR sync) is well under this;
// crossing it usually means a CI loop or a leaked secret.
//
// lastSeen is bumped on every take() so the periodic sweeper can drop
// installations that haven't had a webhook in days. Without that, a
// SaaS instance with thousands of GitHub Apps over its lifetime
// accumulates a permanent map entry per ever-seen installation id.
type ghTokenBucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
	lastSeen   time.Time
}

func (b *ghTokenBucket) take() bool {
	const cap = 60.0
	const refillPerSec = 1.0
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	if !b.lastRefill.IsZero() {
		elapsed := now.Sub(b.lastRefill).Seconds()
		b.tokens += elapsed * refillPerSec
		if b.tokens > cap {
			b.tokens = cap
		}
	} else {
		b.tokens = cap
	}
	b.lastRefill = now
	b.lastSeen = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (h *GithubHandler) allowInstallation(id int64) bool {
	if id == 0 {
		// No installation id — fall through to allow. Setup-related
		// webhooks (`installation`, `installation_repositories`)
		// don't always carry one in the same shape.
		return true
	}
	h.limiterMu.Lock()
	defer h.limiterMu.Unlock()
	if h.installLimiters == nil {
		h.installLimiters = map[int64]*ghTokenBucket{}
	}
	b, ok := h.installLimiters[id]
	if !ok {
		b = &ghTokenBucket{}
		h.installLimiters[id] = b
	}
	return b.take()
}

// gcInstallLimiters drops bucket entries whose lastSeen is older than
// `maxAge`. Cheap (one-pass scan; the map is at most "live
// installations" big in steady state). Called from a 1h ticker started
// by RunInstallLimiterGC.
func (h *GithubHandler) gcInstallLimiters(maxAge time.Duration) int {
	now := time.Now()
	h.limiterMu.Lock()
	defer h.limiterMu.Unlock()
	dropped := 0
	for id, b := range h.installLimiters {
		b.mu.Lock()
		idle := now.Sub(b.lastSeen)
		b.mu.Unlock()
		if idle > maxAge {
			delete(h.installLimiters, id)
			dropped++
		}
	}
	return dropped
}

// RunInstallLimiterGC starts a goroutine that sweeps idle bucket
// entries every hour. Drops entries whose last webhook was 7+ days
// ago. main.go should call this once after wiring the handler.
func (h *GithubHandler) RunInstallLimiterGC(ctx context.Context) {
	go func() {
		t := time.NewTicker(1 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.gcInstallLimiters(7 * 24 * time.Hour)
			}
		}
	}()
}

// MountPublic registers webhook (no JWT) + setup-callback routes onto
// the unauthenticated router group. Webhooks rely on HMAC verification;
// setup-callback handles the GitHub redirect that arrives mid-OAuth.
func (h *GithubHandler) MountPublic(r chi.Router) {
	r.Post("/api/webhooks/github", h.Webhook)
	r.Get("/api/github/setup-callback", h.SetupCallback)
}

// MountAuthed registers the bearer-protected routes. All require
// authentication (the authed group's Issuer.Middleware). The three
// enumeration/disclosure endpoints additionally require settings:admin
// in-handler (see requireAdmin calls below), because they return the
// GitHub App's ENTIRE connected footprint — every installation and every
// repo file tree — which any authenticated viewer must not be able to
// walk. The repo-specific probes (check-repo, detect-runtime,
// scan-addons) stay non-admin: they act on a single repo the caller
// already names while editing a service, so an editor keeps that flow.
//   - /api/github/install-url
//   - /api/github/installations              (admin — enumeration)
//   - /api/github/installations/{id}/repos   (admin — enumeration)
//   - /api/github/installations/refresh (POST)
//   - /api/github/installations/{id}/repos/{owner}/{repo}/tree  (admin — disclosure)
//   - /api/github/detect-runtime (POST)
func (h *GithubHandler) MountAuthed(r chi.Router) {
	r.Get("/api/github/install-url", h.InstallURL)
	r.Get("/api/github/installations", h.ListInstallations)
	r.Get("/api/github/installations/{id}/repos", h.InstallationRepos)
	r.Post("/api/github/installations/refresh", h.RefreshInstallations)
	r.Get("/api/github/installations/{id}/repos/{owner}/{repo}/tree", h.RepoTree)
	r.Post("/api/github/detect-runtime", h.DetectRuntime)
	r.Post("/api/github/scan-addons", h.ScanAddons)
	r.Get("/api/github/webhook-health", h.WebhookHealth)
	// Repo accessibility probe — used by the service-settings UI to
	// validate "can the App actually read this repo before I save?".
	// Same code path as the build-trigger preflight, exposed so the
	// user gets feedback at edit time instead of at next build.
	r.Post("/api/github/check-repo", h.CheckRepoAccess)
}

// CheckRepoAccess returns 200 when the configured installation can
// read the requested repo, 404 / 403 / 502 on the various failure
// modes. Body: {installationId, owner, repo} — when installationId
// is 0 we auto-resolve from the cache, mirroring the build-trigger
// behaviour. Used by the service settings panel's "Test access"
// button.
func (h *GithubHandler) CheckRepoAccess(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil {
		http.Error(w, "github not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		InstallationID int64  `json:"installationId"`
		Owner          string `json:"owner"`
		Repo           string `json:"repo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Owner == "" || body.Repo == "" {
		http.Error(w, "owner + repo required", http.StatusBadRequest)
		return
	}
	if body.InstallationID == 0 && h.Cache != nil {
		// Auto-resolve, same as the build path.
		if id, err := github.ResolveInstallationForRepo(r.Context(), h.Cache, body.Owner, body.Repo); err == nil && id > 0 {
			body.InstallationID = id
		}
	}
	if body.InstallationID == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":    false,
			"error": "no GitHub App installation found for owner " + body.Owner + " — install the kuso App on this account",
		})
		return
	}
	if err := h.Client.CheckRepoAccess(r.Context(), body.InstallationID, body.Owner, body.Repo); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":             false,
			"error":          err.Error(),
			"installationId": body.InstallationID,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":             true,
		"installationId": body.InstallationID,
	})
}

// ScanAddons returns suggested addon kinds based on the repo's
// .env.example / docker-compose hints. Used by the project-creation
// fast path to pre-check checkboxes.
func (h *GithubHandler) ScanAddons(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil {
		http.Error(w, "github not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		InstallationID int64  `json:"installationId"`
		Owner          string `json:"owner"`
		Repo           string `json:"repo"`
		Branch         string `json:"branch"`
		Path           string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.InstallationID == 0 || body.Owner == "" || body.Repo == "" || body.Branch == "" {
		http.Error(w, "installationId, owner, repo, branch required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := h.Client.ScanAddons(ctx, body.InstallationID, body.Owner, body.Repo, body.Branch, body.Path)
	if err != nil {
		h.Logger.Error("github: scan addons", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// Webhook is POST /api/webhooks/github. Reads raw body, verifies
// X-Hub-Signature-256 against GITHUB_APP_WEBHOOK_SECRET, then dispatches.
//
// Returns 204 on success — GitHub doesn't care about body contents.
func (h *GithubHandler) Webhook(w http.ResponseWriter, r *http.Request) {
	if h.Cfg == nil || h.Cfg.WebhookSecret == "" {
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 5<<20)) // 5 MiB ceiling
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	if !github.VerifySignature(h.Cfg.WebhookSecret, body, sig) {
		// Use 400 (not 401) to match the TS server, and to keep our
		// error surface generic — GitHub retries on 5xx but not 4xx.
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	event := r.Header.Get("X-GitHub-Event")
	if event == "" {
		http.Error(w, "missing event", http.StatusBadRequest)
		return
	}
	deliveryID := r.Header.Get("X-GitHub-Delivery")
	// Cheap installation-id sniff. We don't unmarshal the full body
	// twice — the dispatcher will do its own decode. We just want the
	// number for the rate limiter / dedup row.
	var installID int64
	if peek := struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}{}; json.Unmarshal(body, &peek) == nil {
		installID = peek.Installation.ID
	}
	// Per-installation token bucket. Cheap (in-memory, microseconds).
	// 60-burst / 60-per-min steady-state; well above GitHub's normal
	// cadence, well below "leaked secret" levels. Returning 429 is
	// safe — GitHub treats it as a soft fail and retries with backoff,
	// so legitimate bursts catch up after the bucket refills.
	if !h.allowInstallation(installID) {
		h.Logger.Warn("github webhook rate limited", "installation", installID, "event", event)
		http.Error(w, "rate limit", http.StatusTooManyRequests)
		return
	}
	// Replay-protection. GitHub retries failed deliveries reusing
	// the same UUID; recording it on first sight + 200-no-op on
	// repeat keeps a single bad downstream from triggering N
	// duplicate dispatches over 24h.
	if h.DB != nil && deliveryID != "" {
		dedupCtx, dedupCancel := context.WithTimeout(r.Context(), 2*time.Second)
		seen, err := h.DB.SeenGithubDelivery(dedupCtx, deliveryID, event, installID)
		dedupCancel()
		if err != nil {
			// Dedup failure is non-fatal — we'd rather double-fire
			// than drop a webhook on a transient DB hiccup. Log and
			// continue.
			h.Logger.Warn("github delivery dedup", "err", err, "delivery", deliveryID)
		} else if seen {
			h.Logger.Info("github webhook replay (dedup)", "delivery", deliveryID, "event", event)
			w.WriteHeader(http.StatusOK)
			return
		}
	}
	// Stamp "last verified webhook received" so the GitHub settings page
	// can show webhook health in-product instead of punting to GitHub's
	// deliveries page. Best-effort, fire-and-forget — never blocks or
	// fails the delivery.
	if h.DB != nil {
		go func(ev string) {
			sctx, scancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer scancel()
			now := time.Now().UTC().Format(time.RFC3339)
			_ = h.DB.SetSetting(sctx, githubLastDeliveryAtKey, now, "webhook")
			_ = h.DB.SetSetting(sctx, githubLastDeliveryEventKey, ev, "webhook")
		}(event)
	}
	if h.Dispatcher == nil {
		// Verified but nowhere to dispatch — just 204 so GitHub stops
		// retrying. Logged for ops.
		h.Logger.Warn("github webhook accepted but dispatcher is nil", "event", event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Dispatch asynchronously on a detached context. A monorepo with
	// 15+ services can take >10s to fan out (one KusoBuild create per
	// service, each with a clone-token Secret), and GitHub's webhook
	// timeout is 10s — synchronous dispatch turns into a retry storm
	// of duplicate builds. The async path returns 204 immediately so
	// GitHub treats the delivery as successful.
	//
	// Idempotency: build creation is deduped on (project, service,
	// sha) via builds.Service so retries (or the rare case of two
	// webhooks racing) don't double-fire. PR env creation is keyed
	// by PR number so it's naturally idempotent.
	//
	// We capture the body + event by value because the request goroutine
	// may unwind (and the body buffer could be reused) before our
	// goroutine runs.
	parent := h.BaseCtx
	if parent == nil {
		parent = context.Background()
	}
	go func(event string, body []byte) {
		ctx, cancel := context.WithTimeout(parent, 5*time.Minute)
		defer cancel()
		if err := h.Dispatcher.Dispatch(ctx, event, body); err != nil {
			h.Logger.Error("github dispatch", "event", event, "err", err)
		}
	}(event, append([]byte(nil), body...))
	w.WriteHeader(http.StatusNoContent)
}

// Setting keys for webhook-health surfacing (stamped by Webhook).
const (
	githubLastDeliveryAtKey    = "github.lastDeliveryAt"
	githubLastDeliveryEventKey = "github.lastDeliveryEvent"
	// webhookStaleAfter: GitHub deliveries are bursty (only on push/PR),
	// so there's no "expected cadence" to measure against. We surface
	// the timestamp + event verbatim and let the operator judge; we
	// don't mark it stale/unhealthy on a quiet repo. The "configured"
	// signal (is the App + webhook secret set up) is the actionable bit.
)

// WebhookHealth implements GET /api/github/webhook-health — surfaces
// whether the GitHub App is configured and when the last verified
// webhook delivery landed, so an operator can confirm pushes are
// actually reaching kuso without leaving for GitHub's deliveries page.
func (h *GithubHandler) WebhookHealth(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	resp := map[string]any{
		// Configured = the App's webhook secret is set (without it the
		// Webhook handler 503s every delivery).
		"configured": h.Cfg != nil && h.Cfg.WebhookSecret != "",
	}
	if h.DB != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if at, _ := h.DB.GetSetting(ctx, githubLastDeliveryAtKey); at != "" {
			resp["lastDeliveryAt"] = at
		}
		if ev, _ := h.DB.GetSetting(ctx, githubLastDeliveryEventKey); ev != "" {
			resp["lastDeliveryEvent"] = ev
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetupCallback handles the redirect that GitHub fires after a user
// installs (or reinstalls) the App. Refreshes the installation cache
// (best-effort) and redirects to the project-create page so the repo
// picker sees the new installation.
//
// Public route — the user is mid-redirect and may not have the JWT
// cookie attached.
func (h *GithubHandler) SetupCallback(w http.ResponseWriter, r *http.Request) {
	installID := r.URL.Query().Get("installation_id")
	action := r.URL.Query().Get("setup_action")
	h.Logger.Info("github setup-callback", "installation_id", installID, "action", action)
	if h.Client != nil && h.Cache != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := h.Client.RefreshInstallations(ctx, h.Cache); err != nil {
			h.Logger.Warn("github: refresh after setup-callback failed", "err", err)
			// Don't 500 — the install itself happened on GitHub. The next
			// view in the UI can re-trigger the refresh.
		}
	}
	http.Redirect(w, r, "/projects/new?github=installed", http.StatusFound)
}

// InstallURL returns the public install URL + configured-flag.
func (h *GithubHandler) InstallURL(w http.ResponseWriter, _ *http.Request) {
	configured := h.Cfg != nil && h.Cfg.IsConfigured()
	url := ""
	if configured {
		url = h.Cfg.InstallURL()
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": configured, "url": url})
}

// ListInstallations returns the cached installation list.
//
// SCOPE: returns every installation the GitHub App can see, with the
// full repo list per installation. This is intentional for kuso's
// single-tenant model (one team per cluster — Coolify-style, see
// CLAUDE.md). In a hypothetical multi-org setup where users from
// org A shouldn't see org B's repos, this endpoint would need a
// per-user installation filter via the GitHub user-to-installations
// API. Out of scope for v1; multi-tenant is a different product.
func (h *GithubHandler) ListInstallations(w http.ResponseWriter, r *http.Request) {
	// Admin-only: this enumerates every installation and repo the App can
	// see, which a non-admin viewer must not be able to walk. Consistent
	// with WebhookHealth and the whole GithubConfigureHandler.
	if !requireAdmin(w, r) {
		return
	}
	if h.Cache == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := h.Cache.List(ctx)
	if err != nil {
		h.Logger.Error("github: list installations", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	type wireInstall struct {
		ID           int64           `json:"id"`
		AccountLogin string          `json:"accountLogin"`
		AccountType  string          `json:"accountType"`
		AccountID    int64           `json:"accountId"`
		Repos        []db.GithubRepo `json:"repositories"`
	}
	rs := make([]wireInstall, 0, len(out))
	for _, ins := range out {
		var repos []db.GithubRepo
		_ = json.Unmarshal([]byte(ins.RepositoriesJSON), &repos)
		if repos == nil {
			repos = []db.GithubRepo{}
		}
		rs = append(rs, wireInstall{ID: ins.ID, AccountLogin: ins.AccountLogin, AccountType: ins.AccountType, AccountID: ins.AccountID, Repos: repos})
	}
	writeJSON(w, http.StatusOK, rs)
}

// InstallationRepos returns the cached repo list for one installation.
func (h *GithubHandler) InstallationRepos(w http.ResponseWriter, r *http.Request) {
	// Admin-only: lists every repo an installation can see — a disclosure
	// surface, same bar as ListInstallations.
	if !requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if h.Cache == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	repos, err := h.Cache.Repos(ctx, id)
	if err != nil {
		switch {
		case errors.Is(err, db.ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
		default:
			h.Logger.Error("github: installation repos", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

// RefreshInstallations forces a cache refresh from GitHub.
func (h *GithubHandler) RefreshInstallations(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil || h.Cache == nil {
		http.Error(w, "github not configured", http.StatusServiceUnavailable)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := h.Client.RefreshInstallations(ctx, h.Cache); err != nil {
		h.Logger.Error("github: refresh", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// RepoTree walks a repo's git tree at HEAD of branch.
func (h *GithubHandler) RepoTree(w http.ResponseWriter, r *http.Request) {
	// Admin-only: walks any connected repo's file tree — a disclosure
	// surface for source that the caller may have no project relationship
	// to. Same bar as ListInstallations / InstallationRepos.
	if !requireAdmin(w, r) {
		return
	}
	if h.Client == nil {
		http.Error(w, "github not configured", http.StatusServiceUnavailable)
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	branch := r.URL.Query().Get("branch")
	if branch == "" {
		http.Error(w, "branch query param required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := h.Client.ListRepoTree(ctx, id, chi.URLParam(r, "owner"), chi.URLParam(r, "repo"), branch, r.URL.Query().Get("path"))
	if err != nil {
		h.Logger.Error("github: repo tree", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DetectRuntime auto-detects runtime + port from a service's repo+path.
func (h *GithubHandler) DetectRuntime(w http.ResponseWriter, r *http.Request) {
	if h.Client == nil {
		http.Error(w, "github not configured", http.StatusServiceUnavailable)
		return
	}
	var body struct {
		InstallationID int64  `json:"installationId"`
		Owner          string `json:"owner"`
		Repo           string `json:"repo"`
		Branch         string `json:"branch"`
		Path           string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.InstallationID == 0 || body.Owner == "" || body.Repo == "" || body.Branch == "" {
		http.Error(w, "installationId, owner, repo, branch required", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	out, err := h.Client.DetectRuntime(ctx, body.InstallationID, body.Owner, body.Repo, body.Branch, body.Path)
	if err != nil {
		h.Logger.Error("github: detect runtime", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
