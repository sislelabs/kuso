package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/kube"
)

// IncidentAgentSettingsHandler is the admin console for the incident agent:
// the runtime config knobs (Setting kv, hot-reloaded) + write-only secret
// upload (CC creds, Discord). All routes are settings:admin.
type IncidentAgentSettingsHandler struct {
	DB        *db.DB
	Kube      *kube.Client
	Namespace string
	// OnConfigChange invalidates the Manager's config cache so a PUT applies
	// within seconds (wired to incidents.DBConfigProvider.Invalidate).
	OnConfigChange func()
	Logger         *slog.Logger
}

const (
	ccSecretName       = "kuso-incident-agent-cc"
	ccSecretKey        = "credentials.json"
	botSecretName      = "kuso-incident-bot-secrets"
	botConfigName      = "kuso-incident-bot-config"
	botDeploymentName  = "kuso-incident-bot"
	channelConfigKey   = "discord-channel-id"
	discordTokenKey    = "discord-bot-token"
	kusoBotTokenKey    = "kuso-bot-token"
	incidentBotManaged = "kuso-server"
)

func (h *IncidentAgentSettingsHandler) Mount(r chi.Router) {
	r.Get("/api/admin/settings/incident-agent", h.Get)
	r.Put("/api/admin/settings/incident-agent", h.Put)
	r.Put("/api/admin/settings/incident-agent/cc-credentials", h.PutCCCredentials)
	r.Put("/api/admin/settings/incident-agent/discord", h.PutDiscord)
}

func (h *IncidentAgentSettingsHandler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

// incidentAgentStatus is the computed, NON-secret status block. Secret values
// are never returned — only presence + safe metadata.
type incidentAgentStatus struct {
	CCConfigured       bool   `json:"ccConfigured"`
	CCExpiresAt        string `json:"ccExpiresAt,omitempty"`
	CCSubscriptionType string `json:"ccSubscriptionType,omitempty"`
	DiscordConfigured  bool   `json:"discordConfigured"`
	ChannelID          string `json:"channelId,omitempty"` // non-secret
	BotDeployed        bool   `json:"botDeployed"`
	BotReady           bool   `json:"botReady"`
	OpenIncidents      int    `json:"openIncidents"`
}

// Get returns {config, status}.
func (h *IncidentAgentSettingsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	cfg, err := h.DB.GetIncidentAgentConfig(ctx)
	if err != nil {
		h.log().Error("incident-settings: get config", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	status := h.computeStatus(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "status": status})
}

// computeStatus reads the secrets/configmap/deployment for presence + safe
// metadata. Best-effort: any read failure just leaves that field false/empty.
func (h *IncidentAgentSettingsHandler) computeStatus(ctx context.Context) incidentAgentStatus {
	var st incidentAgentStatus
	cs := h.Kube.Clientset

	if sec, err := cs.CoreV1().Secrets(h.Namespace).Get(ctx, ccSecretName, metav1.GetOptions{}); err == nil {
		if raw := sec.Data[ccSecretKey]; len(raw) > 0 {
			st.CCConfigured = true
			st.CCExpiresAt, st.CCSubscriptionType = parseCCMeta(raw)
		}
	}
	if sec, err := cs.CoreV1().Secrets(h.Namespace).Get(ctx, botSecretName, metav1.GetOptions{}); err == nil {
		st.DiscordConfigured = len(sec.Data[discordTokenKey]) > 0 && len(sec.Data[kusoBotTokenKey]) > 0
	}
	if cm, err := cs.CoreV1().ConfigMaps(h.Namespace).Get(ctx, botConfigName, metav1.GetOptions{}); err == nil {
		st.ChannelID = cm.Data[channelConfigKey]
	}
	if dep, err := cs.AppsV1().Deployments(h.Namespace).Get(ctx, botDeploymentName, metav1.GetOptions{}); err == nil {
		st.BotDeployed = true
		st.BotReady = dep.Status.ReadyReplicas > 0
	}
	if n, err := h.DB.CountOpenIncidents(ctx); err == nil {
		st.OpenIncidents = n
	}
	return st
}

// parseCCMeta pulls the NON-secret expiry + subscription type out of a
// claudeAiOauth credentials blob. Returns ("","") on any parse failure.
func parseCCMeta(raw []byte) (expiresAt, subType string) {
	var blob struct {
		ClaudeAiOauth struct {
			ExpiresAt        int64  `json:"expiresAt"`
			SubscriptionType string `json:"subscriptionType"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal(raw, &blob); err != nil {
		return "", ""
	}
	if blob.ClaudeAiOauth.ExpiresAt > 0 {
		// CC stores expiresAt as epoch millis.
		expiresAt = time.UnixMilli(blob.ClaudeAiOauth.ExpiresAt).UTC().Format(time.RFC3339)
	}
	return expiresAt, blob.ClaudeAiOauth.SubscriptionType
}

// Put saves the config knobs + invalidates the Manager cache.
func (h *IncidentAgentSettingsHandler) Put(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var in db.IncidentAgentConfig
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Validate the numeric knobs (0 = "disabled" is legal for both).
	if in.MaxConcurrent < 0 || in.MaxConcurrent > 50 {
		http.Error(w, "maxConcurrent must be 0..50", http.StatusBadRequest)
		return
	}
	if in.CooldownHours < 0 || in.CooldownHours > 168 {
		http.Error(w, "cooldownHours must be 0..168", http.StatusBadRequest)
		return
	}
	if err := h.DB.SetIncidentAgentConfig(ctx, in, userOf(r)); err != nil {
		h.log().Error("incident-settings: put config", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.OnConfigChange != nil {
		h.OnConfigChange()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PutCCCredentials validates + stores the Claude Code creds secret.
func (h *IncidentAgentSettingsHandler) PutCCCredentials(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var body struct {
		Credentials string `json:"credentials"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Credentials) == "" {
		http.Error(w, "credentials required", http.StatusBadRequest)
		return
	}
	// Validate the shape so we never store garbage that just makes every
	// agent run fail auth. Must parse + carry claudeAiOauth.accessToken.
	var probe struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if err := json.Unmarshal([]byte(body.Credentials), &probe); err != nil || probe.ClaudeAiOauth.AccessToken == "" {
		http.Error(w, "not a valid Claude Code credentials blob (need claudeAiOauth.accessToken)", http.StatusBadRequest)
		return
	}
	if err := h.upsertSecret(ctx, ccSecretName, map[string][]byte{ccSecretKey: []byte(body.Credentials)}); err != nil {
		h.log().Error("incident-settings: write cc secret", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// PutDiscord stores the Discord bot config. Each field is optional (only-set
// fields update), so an admin can rotate just the bot token. Restarts the bot
// deployment so it reconnects with the new config.
func (h *IncidentAgentSettingsHandler) PutDiscord(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	var body struct {
		BotToken     string `json:"botToken,omitempty"`
		KusoBotToken string `json:"kusoBotToken,omitempty"`
		ChannelID    string `json:"channelId,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Merge into the existing secret (only overwrite provided fields).
	secData := map[string][]byte{}
	if existing, err := h.Kube.Clientset.CoreV1().Secrets(h.Namespace).Get(ctx, botSecretName, metav1.GetOptions{}); err == nil {
		for k, v := range existing.Data {
			secData[k] = v
		}
	}
	if body.BotToken != "" {
		secData[discordTokenKey] = []byte(body.BotToken)
	}
	if body.KusoBotToken != "" {
		secData[kusoBotTokenKey] = []byte(body.KusoBotToken)
	}
	if len(secData) > 0 {
		if err := h.upsertSecret(ctx, botSecretName, secData); err != nil {
			h.log().Error("incident-settings: write bot secret", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	}
	if body.ChannelID != "" {
		if err := h.upsertConfigMap(ctx, botConfigName, map[string]string{channelConfigKey: body.ChannelID}); err != nil {
			h.log().Error("incident-settings: write bot configmap", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
	}
	// Bounce the bot so it reconnects with the new token/channel.
	h.restartBot(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// upsertSecret creates-or-updates an Opaque secret managed by kuso-server.
func (h *IncidentAgentSettingsHandler) upsertSecret(ctx context.Context, name string, data map[string][]byte) error {
	cs := h.Kube.Clientset.CoreV1().Secrets(h.Namespace)
	existing, err := cs.Get(ctx, name, metav1.GetOptions{})
	sec := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: h.Namespace,
			Labels:    map[string]string{"app.kubernetes.io/managed-by": incidentBotManaged},
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
	if apierrors.IsNotFound(err) {
		_, e := cs.Create(ctx, sec, metav1.CreateOptions{})
		return e
	}
	if err != nil {
		return err
	}
	sec.ResourceVersion = existing.ResourceVersion
	_, e := cs.Update(ctx, sec, metav1.UpdateOptions{})
	return e
}

func (h *IncidentAgentSettingsHandler) upsertConfigMap(ctx context.Context, name string, data map[string]string) error {
	cs := h.Kube.Clientset.CoreV1().ConfigMaps(h.Namespace)
	existing, err := cs.Get(ctx, name, metav1.GetOptions{})
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: h.Namespace},
		Data:       data,
	}
	if apierrors.IsNotFound(err) {
		_, e := cs.Create(ctx, cm, metav1.CreateOptions{})
		return e
	}
	if err != nil {
		return err
	}
	cm.ResourceVersion = existing.ResourceVersion
	_, e := cs.Update(ctx, cm, metav1.UpdateOptions{})
	return e
}

// restartBot bumps a restart annotation on the bot deployment (best-effort).
func (h *IncidentAgentSettingsHandler) restartBot(ctx context.Context) {
	patch := []byte(`{"spec":{"template":{"metadata":{"annotations":{"kuso.sislelabs.com/restartedAt":"` +
		time.Now().UTC().Format(time.RFC3339) + `"}}}}}`)
	_, err := h.Kube.Clientset.AppsV1().Deployments(h.Namespace).Patch(
		ctx, botDeploymentName, "application/strategic-merge-patch+json", patch, metav1.PatchOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		h.log().Warn("incident-settings: restart bot", "err", err)
	}
}

// userOf returns the calling user's id from the JWT claims, "" if absent.
func userOf(r *http.Request) string {
	if c, ok := auth.ClaimsFromContext(r.Context()); ok && c != nil {
		return c.UserID
	}
	return ""
}
