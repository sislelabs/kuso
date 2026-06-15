package handlers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/apimachinery/pkg/api/resource"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
)

// SettingsHandler exposes /api/admin/settings/* — admin-only knobs
// persisted to the Setting table. Today: build resource limits +
// concurrency cap. Future toggles land here without a new package.
type SettingsHandler struct {
	DB     *db.DB
	Logger *slog.Logger

	// OnBuildSettingsChange, when non-nil, is invoked after a
	// successful PutBuild. main.go wires it to
	// builds.Service.InvalidateSettingsCache so the next build picks
	// up the new memory/concurrency knobs immediately instead of
	// waiting for the 30s in-process cache to expire. Without this,
	// an admin who raises a too-low memory limit watches the next
	// 10 builds OOM with the old value.
	OnBuildSettingsChange func()
}

// Mount wires the routes. Read is gated on settings-read perms,
// write requires settings-admin (the kuso-admins group).
func (h *SettingsHandler) Mount(r chi.Router) {
	r.Get("/api/admin/settings/build", h.GetBuild)
	r.Put("/api/admin/settings/build", h.PutBuild)
	r.Get("/api/admin/settings/session", h.GetSession)
	r.Put("/api/admin/settings/session", h.PutSession)
}

// maxSessionTTLSeconds bounds the configurable session lifetime at
// 1 year. Anything longer is effectively "never expire", which has its
// own explicit toggle — capping the numeric field keeps a fat-fingered
// value from minting a de-facto permanent token by accident.
const maxSessionTTLSeconds = 365 * 24 * 60 * 60

// GetSession returns the live session-lifetime view (defaults +
// overrides): how long a dashboard login stays valid, and whether
// expiry is disabled entirely.
func (h *SettingsHandler) GetSession(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := h.DB.GetSessionSettings(ctx)
	if err != nil {
		h.Logger.Error("settings: get session", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PutSession validates + persists the session-lifetime knobs. Takes
// effect on the next login (existing tokens keep their original expiry).
func (h *SettingsHandler) PutSession(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in db.SessionSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// When expiry is on, the TTL must be a sane positive bounded value.
	// (When NeverExpire is true the TTL is ignored, so we don't reject
	// a stale/zero value in that case.)
	if !in.NeverExpire {
		if in.TTLSeconds < 60 {
			http.Error(w, "ttlSeconds must be at least 60 (or enable neverExpire)", http.StatusBadRequest)
			return
		}
		if in.TTLSeconds > maxSessionTTLSeconds {
			http.Error(w, "ttlSeconds capped at 1 year — enable neverExpire for longer", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	updatedBy := ""
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		updatedBy = claims.Username
	}
	if err := h.DB.SetSessionSettings(ctx, in, updatedBy); err != nil {
		h.Logger.Error("settings: put session", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// GetBuild returns the live merged view (defaults + overrides).
func (h *SettingsHandler) GetBuild(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	out, err := h.DB.GetBuildSettings(ctx)
	if err != nil {
		h.Logger.Error("settings: get build", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// PutBuild validates + writes the new values. Quantity strings must
// parse via resource.ParseQuantity so a typo here doesn't break
// every future build with a kube-apiserver validation error.
func (h *SettingsHandler) PutBuild(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var in db.BuildSettings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Validate concurrency cap. 0 disables the cap which is risky
	// on a small box but legitimate on a beefy one — accept and
	// document in the UI.
	if in.MaxConcurrent < 0 {
		http.Error(w, "maxConcurrent must be >= 0", http.StatusBadRequest)
		return
	}
	if in.MaxConcurrent > 32 {
		http.Error(w, "maxConcurrent capped at 32 — open an issue if you need more", http.StatusBadRequest)
		return
	}
	for name, q := range map[string]string{
		"memoryLimit":   in.MemoryLimit,
		"memoryRequest": in.MemoryRequest,
		"cpuLimit":      in.CPULimit,
		"cpuRequest":    in.CPURequest,
	} {
		if q == "" {
			continue
		}
		if _, err := resource.ParseQuantity(q); err != nil {
			http.Error(w, name+": invalid quantity ("+err.Error()+")", http.StatusBadRequest)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	updatedBy := ""
	if claims, ok := auth.ClaimsFromContext(ctx); ok && claims != nil {
		updatedBy = claims.Username
	}
	if err := h.DB.SetBuildSettings(ctx, in, updatedBy); err != nil {
		h.Logger.Error("settings: put build", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if h.OnBuildSettingsChange != nil {
		h.OnBuildSettingsChange()
	}
	writeJSON(w, http.StatusOK, in)
}
