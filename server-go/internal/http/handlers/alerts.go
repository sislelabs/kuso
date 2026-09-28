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
	"strings"
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
	if !requireAdmin(w, r) {
		return
	}
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
	Env             string   `json:"env,omitempty"`
	Query           string   `json:"query,omitempty"`
	ThresholdInt    *int64   `json:"thresholdInt,omitempty"`
	ThresholdFloat  *float64 `json:"thresholdFloat,omitempty"`
	WindowSeconds   int      `json:"windowSeconds,omitempty"`
	Severity        string   `json:"severity,omitempty"`
	ThrottleSeconds int      `json:"throttleSeconds,omitempty"`
}

func (h *AlertsHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body createAlertBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if err := normalizeAlertBody(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rule := db.AlertRule{
		ID:              randomID16(),
		Name:            body.Name,
		Enabled:         true,
		Kind:            body.Kind,
		Project:         body.Project,
		Service:         body.Service,
		Env:             body.Env,
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

// normalizeAlertBody validates a create request and fills per-kind
// defaults, so the stored rule says exactly what the engine evaluates.
func normalizeAlertBody(b *createAlertBody) error {
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" || b.Kind == "" {
		return errors.New("name and kind required")
	}
	episodic := db.IsEpisodicAlertKind(b.Kind)
	switch b.Kind {
	case db.AlertKindLogMatch, db.AlertKindNodeCPU, db.AlertKindNodeMem, db.AlertKindNodeDisk:
	case db.AlertKindHTTP5xxRate:
		b.ThresholdFloat = defaultFloat(b.ThresholdFloat, 5)
		if *b.ThresholdFloat <= 0 || *b.ThresholdFloat > 100 {
			return errors.New("http_5xx_rate threshold is a percentage between 0 and 100")
		}
		b.ThresholdInt = defaultInt(b.ThresholdInt, 20)
	case db.AlertKindHTTPP95Latency:
		b.ThresholdFloat = defaultFloat(b.ThresholdFloat, 1000)
		if *b.ThresholdFloat <= 0 {
			return errors.New("http_p95_latency threshold is milliseconds and must be > 0")
		}
		b.ThresholdInt = defaultInt(b.ThresholdInt, 20)
	case db.AlertKindCertExpiry:
		b.ThresholdInt = defaultInt(b.ThresholdInt, 14)
		if *b.ThresholdInt < 1 || *b.ThresholdInt > 90 {
			return errors.New("cert_expiry threshold is days before expiry, 1-90")
		}
	case db.AlertKindDNSMismatch:
	default:
		return errors.New("kind must be one of log_match|node_cpu|node_mem|node_disk|http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch")
	}
	if b.ThresholdInt != nil && *b.ThresholdInt < 0 {
		return errors.New("thresholdInt must be >= 0")
	}
	// Env scoping only exists for the env-aware kinds; accepting it on
	// a log/node rule would silently widen the rule's scope.
	if b.Env != "" && !episodic {
		return errors.New("env scoping is only supported for http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch")
	}
	if episodic && (b.Service != "" || b.Env != "") && b.Project == "" {
		return errors.New("service/env scoping needs a project")
	}
	// Normalize severity to the canonical info|warn|error set. This
	// matters beyond display: the notify dispatcher's mute carve-out
	// pages through ONLY exact `severity == "error"` alert.fired
	// events — an API/CLI rule stored as "critical" or "Error" would
	// look page-worthy everywhere and silently stay muted.
	switch strings.ToLower(strings.TrimSpace(b.Severity)) {
	case "":
		b.Severity = "warn"
	case "info":
		b.Severity = "info"
	case "warn", "warning":
		b.Severity = "warn"
	case "error", "critical", "crit":
		b.Severity = "error"
	default:
		return errors.New("severity must be one of info|warn|error")
	}
	if b.WindowSeconds <= 0 {
		b.WindowSeconds = 300
	}
	if b.ThrottleSeconds <= 0 {
		b.ThrottleSeconds = 600
	}
	return nil
}

func defaultFloat(p *float64, def float64) *float64 {
	if p != nil {
		return p
	}
	return &def
}

func defaultInt(p *int64, def int64) *int64 {
	if p != nil {
		return p
	}
	return &def
}

func (h *AlertsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
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
	if !requireAdmin(w, r) {
		return
	}
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
		writeErr(w, http.StatusNotFound, notFoundMsg(err, db.ErrAlertNotFound, "alert"))
		return
	}
	h.Logger.Error("alerts handler", "op", op, "err", err)
	writeErr(w, http.StatusInternalServerError, "internal")
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
