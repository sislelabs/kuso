// github_configure.go — admin endpoints for self-service GitHub App
// setup from the dashboard. Lets a user who installed kuso WITHOUT
// `--github-wizard` paste their App credentials later instead of
// reinstalling.
//
// Two endpoints:
//
//	GET  /api/github/setup-status   — current state (configured? slug?)
//	POST /api/github/configure      — write the kuso-github-app Secret +
//	                                  trigger a kuso-server restart so
//	                                  the new env loads cleanly.
//
// Why restart instead of hot-reloading github.Config? The github
// dispatcher holds a *Client transport with a cached App JWT signer,
// the webhook receiver holds the old WebhookSecret, and there are
// other singletons keyed off Config. Hot-swapping all of them mid-
// flight is error-prone. A pod restart is ~30s of downtime in exchange
// for known-good initialization. The wizard UI shows a "restarting…"
// state and polls /healthz until back up.
package handlers

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/auth"
	"kuso/server/internal/kube"
)

// GithubConfigureHandler is intentionally separate from GithubHandler:
// it's mounted unconditionally (even when GitHub isn't configured yet —
// otherwise there's no way to bootstrap from the UI). Holds only the
// kube client so it can write the Secret + restart the deployment.
type GithubConfigureHandler struct {
	Kube      *kube.Client
	Namespace string // kuso namespace, defaults to "kuso"
	Logger    *slog.Logger
}

// Mount wires the routes onto the authed router.
func (h *GithubConfigureHandler) Mount(r chi.Router) {
	r.Get("/api/github/setup-status", h.SetupStatus)
	r.Post("/api/github/configure", h.Configure)
}

// SetupStatus reports whether the App is configured and (if so) which
// slug. Used by /settings/github to render either the wizard form or
// the "already configured" panel with a "Reconfigure" affordance.
func (h *GithubConfigureHandler) SetupStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	out := map[string]any{"configured": false}
	sec, err := h.Kube.Clientset.CoreV1().Secrets(h.namespace()).
		Get(ctx, "kuso-github-app", metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			h.fail(w, "get secret", err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out["configured"] = secretConfigured(sec)
	if v, ok := sec.Data["GITHUB_APP_SLUG"]; ok {
		out["appSlug"] = string(v)
	}
	if v, ok := sec.Data["GITHUB_APP_ID"]; ok {
		out["appId"] = string(v)
	}
	writeJSON(w, http.StatusOK, out)
}

// configureRequest is the wire shape the UI form posts. We accept either
// a literal PEM (with newlines) or a single-line PEM with `\n`s — the
// latter is what shows up if a user copy-pastes into a JSON body. We
// canonicalize before validation.
type configureRequest struct {
	AppID         string `json:"appId"`         // numeric, required
	AppSlug       string `json:"appSlug"`       // required (shown in install URL)
	ClientID      string `json:"clientId"`      // required for OAuth
	ClientSecret  string `json:"clientSecret"`  // required for OAuth
	WebhookSecret string `json:"webhookSecret"` // required for webhook HMAC
	PrivateKey    string `json:"privateKey"`    // PEM, required (App JWT signer)
	Org           string `json:"org"`           // optional, informational
}

// Configure writes the kuso-github-app Secret (creating or updating it)
// then triggers a rollout restart so the new env reaches the running
// pod. Returns 200 when the patch is applied — the caller polls
// /healthz to know when the restart is done.
func (h *GithubConfigureHandler) Configure(w http.ResponseWriter, r *http.Request) {
	if !h.requireAdmin(w, r) {
		return
	}
	var body configureRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := body.validate(); err != nil {
		http.Error(w, "invalid: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Canonicalize the PEM so the file we write is parseable when the
	// pod restarts and LoadConfig reads GITHUB_APP_PRIVATE_KEY.
	pem := strings.ReplaceAll(body.PrivateKey, `\n`, "\n")
	if !strings.HasSuffix(pem, "\n") {
		pem += "\n"
	}

	data := map[string][]byte{
		"GITHUB_APP_ID":             []byte(strings.TrimSpace(body.AppID)),
		"GITHUB_APP_SLUG":           []byte(strings.TrimSpace(body.AppSlug)),
		"GITHUB_APP_CLIENT_ID":      []byte(strings.TrimSpace(body.ClientID)),
		"GITHUB_APP_CLIENT_SECRET":  []byte(strings.TrimSpace(body.ClientSecret)),
		"GITHUB_APP_WEBHOOK_SECRET": []byte(strings.TrimSpace(body.WebhookSecret)),
		"GITHUB_APP_PRIVATE_KEY":    []byte(pem),
	}
	if body.Org != "" {
		data["GITHUB_APP_ORG"] = []byte(strings.TrimSpace(body.Org))
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	if err := h.upsertSecret(ctx, "kuso-github-app", data); err != nil {
		h.fail(w, "upsert secret", err)
		return
	}

	// Restart the kuso-server deployment so the new env loads. We do
	// this by patching a kuso.sislelabs.com/restartedAt annotation —
	// same shape as `kubectl rollout restart` (kubectl uses
	// kubectl.kubernetes.io/restartedAt). Using our own key keeps
	// kubectl history clean.
	patch := fmt.Appendf(nil, `{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":%q}}}}}`,
		time.Now().UTC().Format(time.RFC3339))
	if _, err := h.Kube.Clientset.AppsV1().
		Deployments(h.namespace()).
		Patch(ctx, "kuso-server", types.StrategicMergePatchType, patch, metav1.PatchOptions{}); err != nil {
		// Secret is written but pod won't pick up new env until it
		// happens to restart for another reason. Surface the failure
		// loudly so the user knows to `kubectl rollout restart` manually.
		h.fail(w, "rollout restart", err)
		return
	}

	h.Logger.Info("github app configured",
		"slug", body.AppSlug,
		"app_id", body.AppID,
		"actor", actorName(r.Context()))

	writeJSON(w, http.StatusAccepted, map[string]any{
		"saved":     true,
		"restarted": true,
		"message":   "kuso-server is restarting; poll /healthz until version comes back online (~30s)",
	})
}

// upsertSecret either creates kuso-github-app or merges the new keys
// into the existing one. Strategic merge would be cleaner but the
// CoreV1 Secrets surface doesn't support it, so we Get→Update.
func (h *GithubConfigureHandler) upsertSecret(ctx context.Context, name string, data map[string][]byte) error {
	api := h.Kube.Clientset.CoreV1().Secrets(h.namespace())
	sec, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get %s: %w", name, err)
		}
		// Create.
		_, cerr := api.Create(ctx, &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.namespace()},
			Type:       corev1.SecretTypeOpaque,
			Data:       data,
		}, metav1.CreateOptions{})
		if cerr != nil {
			return fmt.Errorf("create %s: %w", name, cerr)
		}
		return nil
	}
	// Update — overwrite the github-* keys we own, preserve any unrelated
	// keys an admin might have stashed there manually.
	if sec.Data == nil {
		sec.Data = map[string][]byte{}
	}
	for k, v := range data {
		sec.Data[k] = v
	}
	if _, uerr := api.Update(ctx, sec, metav1.UpdateOptions{}); uerr != nil {
		return fmt.Errorf("update %s: %w", name, uerr)
	}
	return nil
}

func (h *GithubConfigureHandler) namespace() string {
	if h.Namespace == "" {
		return "kuso"
	}
	return h.Namespace
}

func (h *GithubConfigureHandler) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	// Use the canonical permission check, not claims.Role == "admin".
	// The string match locked out OAuth-bootstrapped admins (whose
	// role is the group slug, not literally "admin") and let in
	// users with a stale "admin" Role row that no longer carries
	// settings:admin permission. Mirrors the BackupHandler v0.9.4
	// fix.
	return requireAdmin(w, r)
}

func (h *GithubConfigureHandler) fail(w http.ResponseWriter, op string, err error) {
	h.Logger.Error("github configure", "op", op, "err", err)
	// Don't leak raw kube error strings (which expose namespace,
	// SA names, RBAC structure) to the client. Admin-only endpoint
	// but the principle of least exposure stands. Operators read
	// the structured slog above.
	http.Error(w, "internal", http.StatusInternalServerError)
}

func actorName(ctx context.Context) string {
	if c, ok := auth.ClaimsFromContext(ctx); ok && c != nil {
		return c.Username
	}
	return ""
}

// validate performs the cheap, local checks. Catches paste mistakes
// (non-numeric App ID, malformed PEM, blank secrets) before we spend a
// kube round-trip on a secret-write that'll ship a broken App.
func (req *configureRequest) validate() error {
	if strings.TrimSpace(req.AppID) == "" {
		return errors.New("appId required")
	}
	if _, err := strconv.ParseInt(strings.TrimSpace(req.AppID), 10, 64); err != nil {
		return errors.New("appId must be a number (find it on the App settings page in GitHub)")
	}
	if strings.TrimSpace(req.AppSlug) == "" {
		return errors.New("appSlug required (the URL fragment in github.com/apps/<slug>)")
	}
	if strings.TrimSpace(req.ClientID) == "" {
		return errors.New("clientId required")
	}
	if strings.TrimSpace(req.ClientSecret) == "" {
		return errors.New("clientSecret required")
	}
	if strings.TrimSpace(req.WebhookSecret) == "" {
		return errors.New("webhookSecret required (set the same value in GitHub App webhook settings)")
	}
	if strings.TrimSpace(req.PrivateKey) == "" {
		return errors.New("privateKey required (paste the .pem contents)")
	}
	pemStr := strings.ReplaceAll(req.PrivateKey, `\n`, "\n")
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return errors.New("privateKey is not a valid PEM (expected a -----BEGIN ... PRIVATE KEY----- block)")
	}
	// Some App keys are PKCS#1, some are PKCS#8 — accept either.
	if _, e1 := x509.ParsePKCS1PrivateKey(block.Bytes); e1 != nil {
		if _, e8 := x509.ParsePKCS8PrivateKey(block.Bytes); e8 != nil {
			return errors.New("privateKey is a PEM but neither PKCS#1 nor PKCS#8 RSA")
		}
	}
	return nil
}

// secretConfigured reports whether the existing Secret has the minimum
// fields needed to make the App work. We require the same bare set
// LoadConfig requires.
func secretConfigured(s *corev1.Secret) bool {
	if s == nil {
		return false
	}
	for _, k := range []string{"GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY", "GITHUB_APP_WEBHOOK_SECRET"} {
		if v, ok := s.Data[k]; !ok || len(v) == 0 {
			return false
		}
	}
	return true
}
