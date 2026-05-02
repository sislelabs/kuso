package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/config"
	"kuso/server/internal/db"
)

// ConfigHandler exposes /api/config/* routes.
type ConfigHandler struct {
	Cfg    *config.Service
	DB     *db.DB
	Logger *slog.Logger
}

// Mount registers the config routes onto the given chi router.
func (h *ConfigHandler) Mount(r chi.Router) {
	r.Get("/api/config", h.GetSettings)
	r.Post("/api/config", h.UpdateSettings)
	r.Get("/api/config/banner", h.Banner)
	r.Get("/api/config/registry", h.Registry)
	r.Get("/api/config/clusterissuer", h.ClusterIssuer)
	r.Get("/api/config/runpacks", h.ListRunpacks)
	r.Delete("/api/config/runpacks/{id}", h.DeleteRunpack)
	r.Get("/api/config/podsizes", h.ListPodSizes)
	r.Post("/api/config/podsizes", h.CreatePodSize)
	r.Put("/api/config/podsizes/{id}", h.UpdatePodSize)
	r.Delete("/api/config/podsizes/{id}", h.DeletePodSize)
	r.Get("/api/config/templates", h.Templates)
}

func cfgCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// GetSettings returns the cached Kuso CR spec, or {} when admin disabled.
func (h *ConfigHandler) GetSettings(w http.ResponseWriter, r *http.Request) {
	if h.Cfg.Features().AdminDisabled {
		writeJSON(w, http.StatusOK, map[string]any{"settings": map[string]any{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"settings": h.Cfg.Settings(),
		// Secrets surface intentionally omits values — only echoes the
		// keys the operator-side env defines so the UI knows which
		// integrations are configured.
		"secrets": map[string]any{},
	})
}

// UpdateSettings replaces the Kuso CR spec from the body's "settings" key.
func (h *ConfigHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.Cfg.Features().AdminDisabled {
		http.Error(w, "admin disabled", http.StatusForbidden)
		return
	}
	var body struct {
		Settings map[string]any `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Settings == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := cfgCtx(r)
	defer cancel()
	if err := h.Cfg.UpdateSettings(ctx, body.Settings); err != nil {
		switch {
		case errors.Is(err, config.ErrAdminDisabled):
			http.Error(w, "admin disabled", http.StatusForbidden)
		case errors.Is(err, config.ErrNotFound):
			http.Error(w, "kuso CR not found", http.StatusNotFound)
		default:
			h.Logger.Error("config: update", "err", err)
			http.Error(w, "internal", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Banner returns the banner config from the CR spec.
func (h *ConfigHandler) Banner(w http.ResponseWriter, r *http.Request) {
	defaultBanner := map[string]any{
		"show": false, "text": "", "bgcolor": "white", "fontcolor": "white",
	}
	if kusoMap, ok := h.Cfg.Settings()["kuso"].(map[string]any); ok {
		if b, ok := kusoMap["banner"].(map[string]any); ok {
			writeJSON(w, http.StatusOK, b)
			return
		}
	}
	writeJSON(w, http.StatusOK, defaultBanner)
}

// Registry returns the registry config from the CR spec.
func (h *ConfigHandler) Registry(w http.ResponseWriter, r *http.Request) {
	if reg, ok := h.Cfg.Settings()["registry"].(map[string]any); ok {
		writeJSON(w, http.StatusOK, reg)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"enabled": false})
}

// ClusterIssuer returns the configured cluster issuer name.
func (h *ConfigHandler) ClusterIssuer(w http.ResponseWriter, _ *http.Request) {
	issuer := "letsencrypt-prod"
	if v, ok := h.Cfg.Settings()["clusterissuer"].(string); ok && v != "" {
		issuer = v
	}
	writeJSON(w, http.StatusOK, map[string]string{"clusterissuer": issuer})
}

// Templates returns the templates catalog config from the CR spec.
// Templates feature itself is opt-in via KUSO_TEMPLATES_ENABLED, but the
// handler still returns the catalog so the UI can render it disabled.
func (h *ConfigHandler) Templates(w http.ResponseWriter, _ *http.Request) {
	feats := h.Cfg.Features()
	out := map[string]any{
		"enabled":  feats.TemplatesEnabled,
		"catalogs": []any{},
	}
	if t, ok := h.Cfg.Settings()["templates"].(map[string]any); ok {
		if c, ok := t["catalogs"]; ok {
			out["catalogs"] = c
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ListRunpacks returns every Runpack with phases joined.
func (h *ConfigHandler) ListRunpacks(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cfgCtx(r)
	defer cancel()
	out, err := h.DB.ListRunpacks(ctx)
	if err != nil {
		h.Logger.Error("list runpacks", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteRunpack removes a runpack + its phase rows.
func (h *ConfigHandler) DeleteRunpack(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cfgCtx(r)
	defer cancel()
	if err := h.DB.DeleteRunpack(ctx, chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("delete runpack", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListPodSizes returns every PodSize.
func (h *ConfigHandler) ListPodSizes(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cfgCtx(r)
	defer cancel()
	out, err := h.DB.ListPodSizes(ctx)
	if err != nil {
		h.Logger.Error("list pod sizes", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// CreatePodSize inserts a new PodSize.
func (h *ConfigHandler) CreatePodSize(w http.ResponseWriter, r *http.Request) {
	var p db.PodSize
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if p.ID == "" {
		p.ID = randomID()
	}
	ctx, cancel := cfgCtx(r)
	defer cancel()
	if err := h.DB.CreatePodSize(ctx, &p); err != nil {
		h.Logger.Error("create pod size", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

// UpdatePodSize replaces the named PodSize columns.
func (h *ConfigHandler) UpdatePodSize(w http.ResponseWriter, r *http.Request) {
	var p db.PodSize
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	p.ID = chi.URLParam(r, "id")
	ctx, cancel := cfgCtx(r)
	defer cancel()
	if err := h.DB.UpdatePodSize(ctx, &p); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("update pod size", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// DeletePodSize removes a PodSize.
func (h *ConfigHandler) DeletePodSize(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := cfgCtx(r)
	defer cancel()
	if err := h.DB.DeletePodSize(ctx, chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.Logger.Error("delete pod size", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// randomID is a small helper for routes that mint UUID-ish ids when the
// body doesn't provide one.
func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(b[:])
}
