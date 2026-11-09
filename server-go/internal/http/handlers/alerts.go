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
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

type AlertsHandler struct {
	DB     *db.DB
	Logger *slog.Logger
	// Notify sends a rule's test notification. nil = test unavailable.
	Notify *notify.Dispatcher
}

func (h *AlertsHandler) Mount(r chi.Router) {
	r.Get("/api/alerts", h.List)
	r.Post("/api/alerts", h.Create)
	r.Put("/api/alerts/{id}", h.Update)
	r.Delete("/api/alerts/{id}", h.Delete)
	r.Post("/api/alerts/{id}/test", h.Test)
	r.Post("/api/alerts/{id}/enable", h.Enable)
	r.Post("/api/alerts/{id}/disable", h.Disable)
}

func alertsCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

// alertAccess is what the caller may do with alert rules. Instance
// admins manage every rule. Everyone else sees rules scoped to projects
// they can read and manages rules scoped to projects they can edit.
// Rules without a project, and node rules (which ignore the project and
// watch every node), cover the whole cluster and stay admin-only.
type alertAccess struct {
	admin   bool
	tenancy db.GroupTenancy
}

func (a alertAccess) can(project, kind string, minRole db.ProjectRole) bool {
	if a.admin {
		return true
	}
	if project == "" || isNodeAlertKind(kind) {
		return false
	}
	return roleAtLeast(auth.ProjectRoleFor(a.tenancy, project), minRole)
}

func isNodeAlertKind(kind string) bool {
	switch kind {
	case db.AlertKindNodeCPU, db.AlertKindNodeMem, db.AlertKindNodeDisk:
		return true
	}
	return false
}

// alertCaller resolves the caller's alertAccess. On false the response
// is already written.
func (h *AlertsHandler) alertCaller(ctx context.Context, w http.ResponseWriter) (alertAccess, bool) {
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return alertAccess{}, false
	}
	if auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		return alertAccess{admin: true}, true
	}
	tenancy, err := h.DB.ListUserTenancyCached(ctx, claims.UserID)
	if err != nil {
		writeErr(w, http.StatusForbidden, "forbidden")
		return alertAccess{}, false
	}
	return alertAccess{tenancy: tenancy}, true
}

func (h *AlertsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	acc, ok := h.alertCaller(ctx, w)
	if !ok {
		return
	}
	out, err := h.DB.ListAlertRules(ctx)
	if err != nil {
		h.fail(w, "list alerts", err)
		return
	}
	if !acc.admin {
		visible := make([]db.AlertRule, 0, len(out))
		for _, rule := range out {
			if acc.can(rule.Project, rule.Kind, db.ProjectRoleViewer) {
				visible = append(visible, rule)
			}
		}
		out = visible
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
	ctx, cancel := alertsCtx(r)
	defer cancel()
	acc, ok := h.alertCaller(ctx, w)
	if !ok {
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
	if !acc.can(body.Project, body.Kind, db.ProjectRoleEditor) {
		if body.Project == "" || isNodeAlertKind(body.Kind) {
			writeErr(w, http.StatusForbidden, "cluster-wide and node alert rules need an instance admin")
		} else {
			writeErr(w, http.StatusForbidden, fmt.Sprintf("you need editor access on project %q to add its alert rules", body.Project))
		}
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
	if err := h.DB.CreateAlertRule(ctx, rule); err != nil {
		h.fail(w, "create alert", err)
		return
	}
	writeJSON(w, http.StatusCreated, rule)
}

// Update replaces a rule's definition. The caller needs edit rights on the
// rule as it is AND on the scope it moves to, so a rule can't be lifted
// out of (or into) a project the caller doesn't edit.
func (h *AlertsHandler) Update(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	if !h.authorizeRuleWrite(ctx, w, id) {
		return
	}
	acc, ok := h.alertCaller(ctx, w)
	if !ok {
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
	if !acc.can(body.Project, body.Kind, db.ProjectRoleEditor) {
		if body.Project == "" || isNodeAlertKind(body.Kind) {
			writeErr(w, http.StatusForbidden, "cluster-wide and node alert rules need an instance admin")
		} else {
			writeErr(w, http.StatusForbidden, fmt.Sprintf("you need editor access on project %q to add its alert rules", body.Project))
		}
		return
	}
	rule := db.AlertRule{
		ID:              id,
		Name:            body.Name,
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
	if err := h.DB.UpdateAlertRule(ctx, rule); err != nil {
		h.fail(w, "update alert", err)
		return
	}
	out, err := h.DB.GetAlertRule(ctx, id)
	if err != nil {
		h.fail(w, "get alert", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type alertTestResponse struct {
	Results []notify.DeliveryResult `json:"results"`
	// Muted: the project is muted, so a real (non-error) firing of this
	// rule would not reach these channels.
	Muted bool `json:"muted,omitempty"`
}

// Test sends the rule's alert card, marked as a test, to every channel the
// real alert would reach, synchronously, and returns each delivery result.
func (h *AlertsHandler) Test(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	id := chi.URLParam(r, "id")
	if !h.authorizeRuleWrite(ctx, w, id) {
		return
	}
	if h.Notify == nil {
		writeErr(w, http.StatusServiceUnavailable, "notifications aren't configured on this server")
		return
	}
	rule, err := h.DB.GetAlertRule(ctx, id)
	if err != nil {
		h.fail(w, "get alert", err)
		return
	}
	extra := map[string]string{"rule_id": rule.ID, "kind": rule.Kind, "test": "true"}
	if rule.Project != "" {
		extra["project"] = rule.Project
	}
	if rule.Service != "" {
		extra["service"] = rule.Service
	}
	ev := notify.AlertFired(rule.Name, "Test notification for this alert rule, sent from the kuso dashboard. Nothing is wrong.", rule.Severity, extra)
	ev.Title = "Test · " + ev.Title
	results, muted, err := h.Notify.DeliverNow(ctx, ev)
	if err != nil {
		h.fail(w, "test alert", err)
		return
	}
	writeJSON(w, http.StatusOK, alertTestResponse{Results: results, Muted: muted})
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
	case db.AlertKindCrashloop:
		b.ThresholdInt = defaultInt(b.ThresholdInt, 3)
		if *b.ThresholdInt < 1 {
			return errors.New("crashloop threshold is a restart count and must be >= 1")
		}
	case db.AlertKindDeployFailed, db.AlertKindRolloutFailing:
	case db.AlertKindHealthcheckFailing:
		b.ThresholdInt = defaultInt(b.ThresholdInt, 5)
		if *b.ThresholdInt < 1 || *b.ThresholdInt > 1440 {
			return errors.New("healthcheck_failing threshold is minutes failing, 1-1440")
		}
	default:
		return errors.New("kind must be one of log_match|node_cpu|node_mem|node_disk|http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch|crashloop|deploy_failed|healthcheck_failing|rollout_failing")
	}
	if b.ThresholdInt != nil && *b.ThresholdInt < 0 {
		return errors.New("thresholdInt must be >= 0")
	}
	// Env scoping only exists for the env-aware kinds; accepting it on
	// a log/node rule would silently widen the rule's scope.
	if b.Env != "" && !episodic {
		return errors.New("env scoping is only supported for http_5xx_rate|http_p95_latency|cert_expiry|dns_mismatch|crashloop|deploy_failed|healthcheck_failing|rollout_failing")
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

// authorizeRuleWrite gates a mutation of an existing rule. A rule the
// caller can't read answers 404, so ids don't reveal other projects'
// rules; one they can read but not edit answers 403. On false the
// response is already written.
func (h *AlertsHandler) authorizeRuleWrite(ctx context.Context, w http.ResponseWriter, id string) bool {
	acc, ok := h.alertCaller(ctx, w)
	if !ok {
		return false
	}
	if acc.admin {
		return true
	}
	project, kind, err := h.DB.GetAlertRuleScope(ctx, id)
	if err != nil {
		h.fail(w, "alert scope", err)
		return false
	}
	if !acc.can(project, kind, db.ProjectRoleViewer) {
		writeErr(w, http.StatusNotFound, "alert rule not found")
		return false
	}
	if !acc.can(project, kind, db.ProjectRoleEditor) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("you need editor access on project %q to change its alert rules", project))
		return false
	}
	return true
}

func (h *AlertsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := alertsCtx(r)
	defer cancel()
	if !h.authorizeRuleWrite(ctx, w, chi.URLParam(r, "id")) {
		return
	}
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
	if !h.authorizeRuleWrite(ctx, w, chi.URLParam(r, "id")) {
		return
	}
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
