package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/db"
	"kuso/server/internal/reconcilehealth"
	"kuso/server/internal/remediate"
)

// ReconcileHealthHandler exposes the control-plane reconcile-health report and
// one-click remediation. Both routes are admin-only: the report reveals
// cluster-wide failure detail, and remediation mutates live StatefulSets / CR
// annotations. The opt-in unattended loop lives in internal/remediate; these
// endpoints are the operator-initiated (auto=false) path.
type ReconcileHealthHandler struct {
	Scanner *reconcilehealth.Scanner
	// Reports serves GET /reconcile from a short-lived cached scan. nil
	// falls back to a fresh Scanner.Scan per request.
	Reports    *reconcilehealth.CachedScanner
	Remediator *remediate.Remediator
	DB         *db.DB
	// Namespace the scanner sweeps. Empty → "kuso".
	Namespace string
	Logger    *slog.Logger
}

// Mount registers the routes onto the bearer-protected router.
func (h *ReconcileHealthHandler) Mount(r chi.Router) {
	// Cluster-wide reconcile-health rollup (read-only scan of every addon +
	// environment CR's reconcile conditions).
	r.Get("/api/health/reconcile", h.Report)
	// Apply a recognised, data-safe remediation to one issue. Operator-
	// initiated (auto=false), so it can act on Safe AND unsafe-but-confirmed
	// issues; the unattended loop is the only auto=true caller.
	r.Post("/api/health/reconcile/remediate", h.Remediate)
}

func (h *ReconcileHealthHandler) namespace() string {
	if h.Namespace == "" {
		return "kuso"
	}
	return h.Namespace
}

func reconcileHealthCtx(r *http.Request) (context.Context, context.CancelFunc) {
	// The orphan-recreate path deletes a StatefulSet and patches a CR; give
	// it a little more headroom than a plain read.
	return context.WithTimeout(r.Context(), 30*time.Second)
}

// Report runs the Scanner and returns the *Report. Admin-only.
func (h *ReconcileHealthHandler) Report(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := reconcileHealthCtx(r)
	defer cancel()
	scan := h.Scanner.Scan
	if h.Reports != nil {
		scan = h.Reports.Scan
	}
	rep, err := scan(ctx, h.namespace())
	if err != nil {
		h.Logger.Error("reconcile-health scan", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// remediateRequest is the wire body for POST /remediate. The minimum is
// {resource}: the handler re-scans to find the matching issue (so the UI can
// fire a one-click button knowing only the row's resource name). namespace,
// type and kind narrow the match when a name alone is ambiguous; when action
// is omitted the scanned issue's recommended Action is used.
type remediateRequest struct {
	Resource  string `json:"resource"`
	Namespace string `json:"namespace,omitempty"`
	Type      string `json:"type,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Action    string `json:"action,omitempty"`
}

// matchRemediateIssue picks the scanned issue body names. A bare resource
// name can match issues in several namespaces, on both an addon and an env,
// or several kinds on one CR. When it does and exactly one of them has a
// remediation, that one is meant; otherwise the caller must narrow it.
func matchRemediateIssue(issues []reconcilehealth.Issue, body remediateRequest) (*reconcilehealth.Issue, int, string) {
	var hits []*reconcilehealth.Issue
	for i := range issues {
		is := &issues[i]
		if is.Resource != body.Resource ||
			body.Namespace != "" && is.Namespace != body.Namespace ||
			body.Type != "" && is.Type != body.Type ||
			body.Kind != "" && string(is.Kind) != body.Kind {
			continue
		}
		hits = append(hits, is)
	}
	if len(hits) > 1 {
		var actionable []*reconcilehealth.Issue
		for _, is := range hits {
			if is.Action != reconcilehealth.ActionNone {
				actionable = append(actionable, is)
			}
		}
		if len(actionable) == 1 {
			hits = actionable
		}
	}
	switch len(hits) {
	case 0:
		// Already healthy, or the name was wrong. 404 so the UI refreshes.
		return nil, http.StatusNotFound, "no outstanding reconcile-health issue for that resource"
	case 1:
		return hits[0], 0, ""
	}
	opts := make([]string, 0, len(hits))
	for _, is := range hits {
		opts = append(opts, fmt.Sprintf("namespace=%s type=%s kind=%s", is.Namespace, is.Type, is.Kind))
	}
	return nil, http.StatusConflict, fmt.Sprintf("%d reconcile-health issues match %q; pass namespace, type or kind to pick one: %s",
		len(hits), body.Resource, strings.Join(opts, "; "))
}

// Remediate applies the recognised remediation for one issue. Admin-only.
// Operator-initiated (auto=false), so a confirmed unsafe issue (e.g.
// spec-mismatch) can be acted on here — that judgement is the admin's, made by
// clicking the button. The unattended loop is the only path that runs with
// auto=true (and therefore refuses unsafe issues).
func (h *ReconcileHealthHandler) Remediate(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	var body remediateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if body.Resource == "" {
		writeErr(w, http.StatusBadRequest, "resource is required")
		return
	}

	ctx, cancel := reconcileHealthCtx(r)
	defer cancel()

	// Re-scan and locate the live issue for this resource. Scanning afresh
	// (rather than trusting a client-supplied issue) keeps the decision of
	// what's safe/which Action applies on the server, where the
	// classification logic lives — the client only names a row.
	rep, err := h.Scanner.Scan(ctx, h.namespace())
	if err != nil {
		h.Logger.Error("reconcile-health scan (remediate)", "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
		return
	}
	iss, status, msg := matchRemediateIssue(rep.Issues, body)
	if iss == nil {
		writeErr(w, status, msg)
		return
	}

	// An explicit action overrides the recommended one (e.g. an operator
	// chooses force_reconcile over the default). Validate it's one the
	// remediator knows; Apply also guards this, but a 400 here is clearer.
	if body.Action != "" {
		act := reconcilehealth.Action(body.Action)
		if act != reconcilehealth.ActionOrphanRecreate && act != reconcilehealth.ActionForceReconcile {
			writeErr(w, http.StatusBadRequest, "unknown remediation action")
			return
		}
		iss.Action = act
	}

	user := auditUser(ctx)
	res, err := h.Remediator.Apply(ctx, *iss, user, false /*operator-initiated, not auto*/)
	if err != nil {
		h.Logger.Error("reconcile-health remediate", "resource", body.Resource, "action", iss.Action, "err", err)
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
