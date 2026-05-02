package handlers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"kuso/server/internal/github"
)

// GithubHandler exposes the webhook receiver. Install/installations
// admin routes can be added later — webhook is the load-bearing one.
type GithubHandler struct {
	Cfg        *github.Config
	Dispatcher *github.Dispatcher
	Logger     *slog.Logger
}

// MountPublic registers webhook (no JWT) routes. Webhooks must reach the
// server without a bearer token — HMAC verification is the auth.
func (h *GithubHandler) MountPublic(mux interface {
	Post(string, http.HandlerFunc)
}) {
	mux.Post("/api/webhooks/github", h.Webhook)
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

