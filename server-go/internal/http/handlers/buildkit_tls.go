package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/buildkittls"
)

// BuildkitTLSHandler is the one-step switch for the build daemon's client
// authentication. Installs that predate it run the daemon unauthenticated
// until an operator flips this; nothing turns it on during an upgrade.
type BuildkitTLSHandler struct {
	Store  *buildkittls.Store
	Audit  *audit.Service
	Logger *slog.Logger
}

func (h *BuildkitTLSHandler) Mount(r chi.Router) {
	r.Get("/api/system/buildkit-tls", h.Get)
	r.Post("/api/system/buildkit-tls", h.Set)
}

func (h *BuildkitTLSHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	on, err := h.Store.Enabled(ctx)
	if err != nil {
		h.Logger.Error("buildkit tls: read", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"enabled": on})
}

func (h *BuildkitTLSHandler) Set(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Enabled == nil {
		writeErr(w, http.StatusBadRequest, `body must be {"enabled": true|false}`)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := h.Store.SetEnabled(ctx, *body.Enabled); err != nil {
		if errors.Is(err, buildkittls.ErrDaemonPredatesTLS) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		h.Logger.Error("buildkit tls: set", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: "critical",
		Action:   "buildkit.tls",
		Resource: "kuso-buildkitd",
		Message:  "build daemon client auth set to " + strconv.FormatBool(*body.Enabled) + "; daemon restarted",
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": *body.Enabled,
		"note":    "kuso-buildkitd is restarting; builds running right now are interrupted and can be re-triggered",
	})
}
