package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
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
}

// MountPublic registers webhook (no JWT) + setup-callback routes onto
// the unauthenticated router group. Webhooks rely on HMAC verification;
// setup-callback handles the GitHub redirect that arrives mid-OAuth.
func (h *GithubHandler) MountPublic(r chi.Router) {
	r.Post("/api/webhooks/github", h.Webhook)
	r.Get("/api/github/setup-callback", h.SetupCallback)
}

// MountAuthed registers the bearer-protected admin routes:
//   - /api/github/install-url
//   - /api/github/installations
//   - /api/github/installations/{id}/repos
//   - /api/github/installations/refresh (POST)
//   - /api/github/installations/{id}/repos/{owner}/{repo}/tree
//   - /api/github/detect-runtime (POST)
func (h *GithubHandler) MountAuthed(r chi.Router) {
	r.Get("/api/github/install-url", h.InstallURL)
	r.Get("/api/github/installations", h.ListInstallations)
	r.Get("/api/github/installations/{id}/repos", h.InstallationRepos)
	r.Post("/api/github/installations/refresh", h.RefreshInstallations)
	r.Get("/api/github/installations/{id}/repos/{owner}/{repo}/tree", h.RepoTree)
	r.Post("/api/github/detect-runtime", h.DetectRuntime)
	r.Post("/api/github/scan-addons", h.ScanAddons)
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
	if h.Dispatcher == nil {
		// Verified but nowhere to dispatch — just 204 so GitHub stops
		// retrying. Logged for ops.
		h.Logger.Warn("github webhook accepted but dispatcher is nil", "event", event)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.Dispatcher.Dispatch(ctx, event, body); err != nil {
		h.Logger.Error("github dispatch", "event", event, "err", err)
		// 500 makes GitHub retry. We trust idempotency in the dispatch
		// path (build creation is keyed by SHA, env creation by PR
		// number, both no-op or recreate cleanly).
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
func (h *GithubHandler) ListInstallations(w http.ResponseWriter, r *http.Request) {
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
