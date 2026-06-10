package handlers

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/audit"
	"kuso/server/internal/db"
	"kuso/server/internal/incidents"
)

// IncidentsHandler exposes the /api/incidents surface for the autonomous
// incident-response agent.
//
// Two audiences, two auth models:
//
//   - OPERATOR endpoints (list, detail, feedback, resolve) ride the normal
//     bearer-protected router and gate on settings:admin via requireAdmin.
//     They're mounted with Mount(r).
//   - AGENT endpoints (findings, pr) authenticate with the per-incident
//     bearer token minted into the Job env. The agent never holds a JWT, so
//     these live on the PUBLIC router and self-gate via incidentTokenAuth.
//     They're mounted with MountPublic(r).
//
// See docs/superpowers/specs/2026-06-10-incident-agent-design.md.
type IncidentsHandler struct {
	DB      *db.DB
	Manager *incidents.Manager
	Audit   *audit.Service
	Logger  *slog.Logger
}

// Mount registers the operator-facing routes on the bearer-protected
// router. Each handler re-checks settings:admin (the agent endpoints are
// NOT here — they're on the public router via MountPublic).
func (h *IncidentsHandler) Mount(r chi.Router) {
	r.Get("/api/incidents", h.List)
	r.Get("/api/incidents/{id}", h.Get)
	r.Post("/api/incidents/{id}/feedback", h.Feedback)
	r.Post("/api/incidents/{id}/resolve", h.Resolve)
	// The Discord bot records the thread it created for an incident here
	// (admin-token authed, same as the bot's other API calls).
	r.Post("/api/incidents/{id}/thread", h.Thread)
}

// MountPublic registers the agent-facing routes on the public router.
// These do NOT pass through the JWT middleware; they authenticate against
// the per-incident bearer token in the Authorization header.
func (h *IncidentsHandler) MountPublic(r chi.Router) {
	r.Post("/api/incidents/{id}/findings", h.Findings)
	r.Post("/api/incidents/{id}/pr", h.PR)
}

func (h *IncidentsHandler) log() *slog.Logger {
	if h.Logger != nil {
		return h.Logger
	}
	return slog.Default()
}

func incidentCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 10*time.Second)
}

// --- operator endpoints ---

// List returns the newest incidents (UI feed). Admin-only.
func (h *IncidentsHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := incidentCtx(r)
	defer cancel()
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.DB.ListIncidents(ctx, limit)
	if err != nil {
		h.log().Error("incidents: list", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []db.Incident{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"incidents": rows})
}

// Get returns one incident by id. Admin-only.
func (h *IncidentsHandler) Get(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := incidentCtx(r)
	defer cancel()
	in, err := h.DB.GetIncident(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrIncidentNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		h.log().Error("incidents: get", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, in)
}

// feedbackRequest is the body the bot/operator posts to /feedback. Either
// Text (a free-text reply that loops back into investigation) or Decision
// ("go" → implement, "reject" → reject) is set.
type feedbackRequest struct {
	Text     string `json:"text"`
	Decision string `json:"decision"`
}

// feedbackAction is the pure routing verdict for a /feedback body. Kept as
// a free function so the switch is unit-testable without HTTP or DB.
type feedbackAction int

const (
	feedbackGo      feedbackAction = iota // operator approved → spawn implement
	feedbackReject                        // operator rejected → close as rejected
	feedbackComment                       // free-text → append + (TODO) re-investigate
)

// resolveFeedbackAction maps a decision string onto the action. Empty /
// unknown decision falls through to a comment (so a {text} reply works and
// a typo'd decision doesn't silently approve a write).
func resolveFeedbackAction(decision string) feedbackAction {
	switch strings.ToLower(strings.TrimSpace(decision)) {
	case "go", "approve", "approved":
		return feedbackGo
	case "reject", "rejected", "no":
		return feedbackReject
	default:
		return feedbackComment
	}
}

// Feedback handles operator feedback on an incident. Admin-only.
func (h *IncidentsHandler) Feedback(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := incidentCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")

	var body feedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Confirm the incident exists up front so all three branches 404
	// consistently rather than each discovering it independently.
	if _, err := h.DB.GetIncident(ctx, id); err != nil {
		if errors.Is(err, db.ErrIncidentNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log().Error("incidents: feedback load", "err", err, "id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}

	switch resolveFeedbackAction(body.Decision) {
	case feedbackGo:
		if h.Manager == nil {
			http.Error(w, "incident agent not configured", http.StatusServiceUnavailable)
			return
		}
		if err := h.Manager.SpawnImplementFor(ctx, id); err != nil {
			h.log().Error("incidents: spawn implement", "err", err, "id", id)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		h.audit(ctx, r, "incident.approve", id, "operator approved → implementing")
	case feedbackReject:
		if err := h.DB.SetIncidentState(ctx, id, db.IncidentRejected); err != nil {
			h.log().Error("incidents: reject", "err", err, "id", id)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		h.audit(ctx, r, "incident.reject", id, "operator rejected incident")
	case feedbackComment:
		fb := db.IncidentFeedback{At: time.Now().UTC(), Text: body.Text}
		if err := h.DB.AppendIncidentFeedback(ctx, id, fb); err != nil {
			h.log().Error("incidents: append feedback", "err", err, "id", id)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		// TODO(incident-agent): re-spawn the investigate Job with the
		// accumulated feedback so the agent refines its findings. For now
		// the feedback is recorded; the operator can still approve/reject.
		h.audit(ctx, r, "incident.feedback", id, "operator feedback recorded")
	}

	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Resolve closes an incident as resolved. Admin-only.
func (h *IncidentsHandler) Resolve(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := incidentCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	if err := h.DB.SetIncidentState(ctx, id, db.IncidentResolved); err != nil {
		if errors.Is(err, db.ErrIncidentNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log().Error("incidents: resolve", "err", err, "id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.audit(ctx, r, "incident.resolve", id, "incident resolved")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// Thread records the Discord thread id the bot created for an incident, so
// a bot restart re-adopts it and the server can route replies. Admin-token
// authed (the bot uses an admin-scoped kuso token).
func (h *IncidentsHandler) Thread(w http.ResponseWriter, r *http.Request) {
	if !requireAdmin(w, r) {
		return
	}
	ctx, cancel := incidentCtx(r)
	defer cancel()
	id := chi.URLParam(r, "id")
	var req struct {
		Thread string `json:"thread"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Thread == "" {
		http.Error(w, "thread required", http.StatusBadRequest)
		return
	}
	if err := h.DB.SetIncidentThread(ctx, id, req.Thread); err != nil {
		if errors.Is(err, db.ErrIncidentNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log().Error("incidents: set thread", "err", err, "id", id)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- agent endpoints (per-incident bearer token auth) ---

// bearerToken extracts the token from an `Authorization: Bearer <tok>`
// header. Returns "" when the header is absent or malformed.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(h[len(prefix):])
}

// incidentTokenAuth reports whether the request carries the per-incident
// bearer token matching in.AgentToken. Constant-time compare; an empty
// stored token or empty presented token never passes (a freshly-created
// incident without a token must not be writable by an unauthenticated
// caller).
func incidentTokenAuth(r *http.Request, in db.Incident) bool {
	presented := bearerToken(r)
	if presented == "" || in.AgentToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(in.AgentToken)) == 1
}

// loadIncidentForAgent loads the incident named in the path and verifies
// the bearer token. On any failure it writes the response (404 for an
// unknown id, 401 for a bad/missing token) and returns ok=false.
func (h *IncidentsHandler) loadIncidentForAgent(w http.ResponseWriter, r *http.Request, ctx context.Context) (db.Incident, bool) {
	in, err := h.DB.GetIncident(ctx, chi.URLParam(r, "id"))
	if errors.Is(err, db.ErrIncidentNotFound) {
		http.Error(w, "not found", http.StatusNotFound)
		return db.Incident{}, false
	}
	if err != nil {
		h.log().Error("incidents: agent load", "err", err)
		http.Error(w, "internal", http.StatusInternalServerError)
		return db.Incident{}, false
	}
	if !incidentTokenAuth(r, in) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return db.Incident{}, false
	}
	return in, true
}

// findingsRequest is the agent's investigation writeup.
type findingsRequest struct {
	Findings string `json:"findings"`
}

// Findings records the agent's investigation writeup and moves the
// incident to awaiting_feedback. Authenticated by the per-incident token.
func (h *IncidentsHandler) Findings(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := incidentCtx(r)
	defer cancel()
	in, ok := h.loadIncidentForAgent(w, r, ctx)
	if !ok {
		return
	}

	// Only an actively-investigating incident accepts findings. A late
	// Job (e.g. re-run after the operator already resolved/rejected) must
	// not re-open a closed incident or clobber a pr_open one.
	if in.State != db.IncidentInvestigating {
		http.Error(w, "incident is not investigating (state="+in.State+")", http.StatusConflict)
		return
	}

	var body findingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.Findings) == "" {
		http.Error(w, "findings required", http.StatusBadRequest)
		return
	}

	if err := h.DB.SetIncidentFindings(ctx, in.ID, body.Findings, db.IncidentAwaitingFeedback); err != nil {
		if errors.Is(err, db.ErrIncidentNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log().Error("incidents: set findings", "err", err, "id", in.ID)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.auditFor(ctx, in, "incident.findings", "agent posted findings")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// prRequest is what the agent posts after pushing its branch + opening the
// PR via the gh CLI (it has the short-lived installation token). The agent
// reports the PR it opened; the server just records it.
//
// TODO(incident-agent): an alternate flow accepts {branch,title,body} and
// has the SERVER open the PR via github.Client.OpenPR (keeping the GitHub
// App as the single push identity). When that's wired, branch the handler
// on which set of fields is present and call the github client here. For v1
// we take the simpler {prUrl, prNumber} shape — the agent has already done
// the push.
type prRequest struct {
	PRUrl    string `json:"prUrl"`
	PRNumber int    `json:"prNumber"`

	// Reserved for the server-opens-PR flow (see TODO above); ignored today.
	Branch string `json:"branch,omitempty"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body,omitempty"`
}

// PR records the PR the agent opened and moves the incident to pr_open.
// Authenticated by the per-incident token.
func (h *IncidentsHandler) PR(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := incidentCtx(r)
	defer cancel()
	in, ok := h.loadIncidentForAgent(w, r, ctx)
	if !ok {
		return
	}
	// Only an implementing incident accepts a PR report.
	if in.State != db.IncidentImplementing {
		http.Error(w, "incident is not implementing (state="+in.State+")", http.StatusConflict)
		return
	}

	var body prRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// v1: the agent already pushed + opened the PR via gh; it reports the
	// URL/number. Require prUrl so we never record a bogus pr_open.
	//
	// TODO(incident-agent): if body.PRUrl == "" but body.Branch != "",
	// resolve the project repo, call github.Client.OpenPR(ctx, instID,
	// owner, repo, body.Branch, base, body.Title, body.Body), and use the
	// returned (url, number). That keeps the App as the only push identity.
	if strings.TrimSpace(body.PRUrl) == "" {
		http.Error(w, "prUrl required", http.StatusBadRequest)
		return
	}

	if err := h.DB.SetIncidentPR(ctx, in.ID, body.PRUrl, body.PRNumber); err != nil {
		if errors.Is(err, db.ErrIncidentNotFound) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		h.log().Error("incidents: set pr", "err", err, "id", in.ID)
		http.Error(w, "internal", http.StatusInternalServerError)
		return
	}
	h.auditFor(ctx, in, "incident.pr", "agent opened PR "+body.PRUrl)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- audit helpers ---

// audit logs an operator-initiated incident action, attributing it to the
// JWT caller. No-op when the audit service is nil/disabled.
func (h *IncidentsHandler) audit(ctx context.Context, r *http.Request, action, id, msg string) {
	if h.Audit == nil {
		return
	}
	in, err := h.DB.GetIncident(ctx, id)
	if err != nil {
		// Best-effort attribution context; still log the action.
		in = db.Incident{ID: id}
	}
	h.Audit.Log(ctx, audit.Entry{
		User:     actingUserID(r),
		Action:   action,
		Pipeline: in.Project,
		App:      in.Service,
		Resource: "incident/" + id,
		Message:  msg,
	})
}

// auditFor logs an agent-initiated incident action (no JWT caller — the
// actor is the agent Job, attributed to the system user).
func (h *IncidentsHandler) auditFor(ctx context.Context, in db.Incident, action, msg string) {
	if h.Audit == nil {
		return
	}
	h.Audit.Log(ctx, audit.Entry{
		Action:   action,
		Pipeline: in.Project,
		App:      in.Service,
		Resource: "incident/" + in.ID,
		Message:  msg,
	})
}
