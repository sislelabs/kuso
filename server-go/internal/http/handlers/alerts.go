// Alert rule CRUD. The engine runs separately (internal/alerts);
// this handler is just storage + UI. Toggle endpoint avoids needing
// a full PATCH for the common enable/disable case.

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

	"kuso/server/internal/db"
)

type AlertsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
}

func (h *AlertsHandler) Mount(r chi.Router) {
	r.Get("/api/alerts", h.List)
	r.Post("/api/alerts", h.Create)
	r.Delete("/api/alerts/{id}", h.Delete)
	r.Post("/api/alerts/{id}/enable", h.Enable)
	r.Post("/api/alerts/{id}/disable", h.Disable)
}

func alertsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *AlertsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	out, err := h.DB.ListAlertRules(ctx)
	if err != nil {
		h.fail(w, "list alerts", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type createAlertBody struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Project         string   `json:"project,omitempty"`
	Service         string   `json:"service,omitempty"`
	Query           string   `json:"query,omitempty"`
	ThresholdInt    *int64   `json:"thresholdInt,omitempty"`
	ThresholdFloat  *float64 `json:"thresholdFloat,omitempty"`
	WindowSeconds   int      `json:"windowSeconds,omitempty"`
	Severity        string   `json:"severity,omitempty"`
	ThrottleSeconds int      `json:"throttleSeconds,omitempty"`
}

func (h *AlertsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createAlertBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Name == "" || body.Kind == "" {
		http.Error(w, "name and kind required", http.StatusBadRequest)
		return
	}
	switch body.Kind {
	case db.AlertKindLogMatch, db.AlertKindNodeCPU, db.AlertKindNodeMem, db.AlertKindNodeDisk:
	default:
		http.Error(w, "kind must be one of log_match|node_cpu|node_mem|node_disk", http.StatusBadRequest)
		return
	}
	if body.Severity == "" {
		body.Severity = "warn"
	}
	if body.WindowSeconds <= 0 {
		body.WindowSeconds = 300
	}
	if body.ThrottleSeconds <= 0 {
		body.ThrottleSeconds = 600
	}
	rule := db.AlertRule{
		ID:              randomID16(),
		Name:            body.Name,
		Enabled:         true,
		Kind:            body.Kind,
		Project:         body.Project,
		Service:         body.Service,
		Query:           body.Query,
		ThresholdInt:    body.ThresholdInt,
		ThresholdFloat:  body.ThresholdFloat,
		WindowSeconds:   body.WindowSeconds,
		Severity:        body.Severity,
		ThrottleSeconds: body.ThrottleSeconds,
	}
	ctx, cancel := alertsCtx(r)
	defer cancel()
	if err := h.DB.CreateAlertRule(ctx, rule); err != nil {
		h.fail(w, "create alert", err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

func (h *AlertsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	if err := h.DB.DeleteAlertRule(ctx, chi.URLParam(r, "id")); err != nil {
		h.fail(w, "delete alert", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertsHandler) Enable(w http.ResponseWriter, r *http.Request) {
	h.toggle(w, r, true)
}

func (h *AlertsHandler) Disable(w http.ResponseWriter, r *http.Request) {
	h.toggle(w, r, false)
}

func (h *AlertsHandler) toggle(w http.ResponseWriter, r *http.Request, on bool) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	if err := h.DB.SetAlertEnabled(ctx, chi.URLParam(r, "id"), on); err != nil {
		h.fail(w, "toggle alert", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertsHandler) fail(w http.ResponseWriter, op string, err error) {
	if errors.Is(err, db.ErrAlertNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	h.Logger.Error("alerts handler", "op", op, "err", err)
	http.Error(w, "internal", http.StatusInternalServerError)
}

// randomID16 — duplicated from ssh_keys.go to avoid an import dance.
// Stable hex slug, used as the AlertRule primary key.
func randomID16Alerts() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Local re-export wrapping the existing helper from ssh_keys.go.
// Keeps both files independent so reordering doesn't break compile.
var _ = randomID16Alerts
