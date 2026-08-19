package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"kuso/server/internal/auth"
	"kuso/server/internal/db"
	"kuso/server/internal/notify"
)

// NotificationsHandler handles /api/notifications full CRUD.
//
// Wire shape mirrors the TS controller:
//
//	{ success: true, data: ..., message?: string }
//
// We keep that envelope so the Vue store doesn't need a remap.
// notifySink is the minimal interface the handler needs from the
// notify dispatcher (avoids importing the full type into router/Deps).
//
// InvalidateNotifications is a hot-path optimisation: the dispatcher
// caches its view of the notifications table for notifsCacheTTL so
// per-event SQLite SELECTs stop contending with the single-writer
// connection. Every CRUD on this handler calls Invalidate so admins
// see channel changes apply immediately to subsequent events.
type notifySink interface {
	EmitEnvelope(notify.EmitEnvelope)
	SendDirect(ctx context.Context, n *db.Notification, e notify.Event) error
	InvalidateNotifications()
}

type NotificationsHandler struct {
	Notify notifySink

	DB     *db.DB
	Logger *slog.Logger

	// ProjectExists guards MuteProject against minting mute rows for
	// names that aren't projects: admins bypass requireProjectAccess,
	// so without this a typo'd PUT inserts a junk row that silently
	// pre-mutes any FUTURE project created under that name. Nil (no
	// kube wiring, tests) skips the check.
	ProjectExists func(ctx context.Context, project string) (bool, error)
}

// Mount registers the routes onto the bearer-protected router.
// CRUD on notification channels + the unread-tracking feed remain
// admin-only. /my-feed is a read-only project-scoped variant for
// every authenticated user — non-admin project devs need deploy
// feedback after closing the service overlay too, and the previous
// admin-only-bell hid the global feed from them entirely.
func (h *NotificationsHandler) Mount(r chi.Router) {
	// Project-scoped read-only feed for any authenticated caller.
	// Resolves the caller's project memberships and returns only
	// events whose project matches. Admins fall through to the
	// admin handler when they prefer (or use this one for an audit-
	// style read).
	r.Get("/api/notifications/my-feed", h.MyFeed)

	r.Group(func(r chi.Router) {
		r.Use(AdminOnly)
		r.Get("/api/notifications", h.List)
		r.Get("/api/notifications/{id}", h.Get)
		r.Post("/api/notifications", h.Create)
		r.Put("/api/notifications/{id}", h.Update)
		r.Delete("/api/notifications/{id}", h.Delete)
		r.Post("/api/notifications/{id}/test", h.Test)
		// Admin-only in-app feed with global readAt tracking. The
		// per-user readAt model doesn't exist yet — the column is a
		// single global flag — so non-admins use /my-feed (no read
		// tracking) instead of seeing stale read state from admins.
		r.Get("/api/notifications/feed", h.Feed)
		r.Get("/api/notifications/feed/unread-count", h.FeedUnread)
		r.Post("/api/notifications/feed/read-all", h.FeedReadAll)
		r.Delete("/api/notifications/feed", h.FeedClear)
		// Outbox health: pending + dead-letter counts surface a red
		// badge on the Settings → Notifications card when webhooks
		// have been failing past the retry cap. Without a UI signal,
		// operators only learn from kuso_notify_outbox_dead Prometheus
		// alerts (and most installs don't ship Prom).
		r.Get("/api/notifications/outbox-stats", h.OutboxStats)
		// Muted-projects roster for the settings page. The per-project
		// mute toggle lives on the project-scoped routes below.
		r.Get("/api/notifications/muted-projects", h.ListMutedProjects)
	})

	// Per-project notification mute. Project editors can silence their
	// own project's external channel delivery (Discord/Slack/webhook/…);
	// the bell feed keeps recording muted projects' events, so this is
	// a "stop pinging us" switch, not an audit-trail eraser.
	r.Get("/api/projects/{project}/notifications/mute", h.GetProjectMute)
	r.Put("/api/projects/{project}/notifications/mute", h.MuteProject)
	r.Delete("/api/projects/{project}/notifications/mute", h.UnmuteProject)
}

// ListMutedProjects returns every muted project (admin-only, feeds the
// settings page roster).
func (h *NotificationsHandler) ListMutedProjects(w http.ResponseWriter, r *http.Request) {
	mutes, err := h.DB.ListProjectNotificationMutes(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list muted projects: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mutes)
}

// GetProjectMute reports whether one project is muted. Any member can
// read it (the project settings page shows the toggle state to viewers
// too; flipping it needs editor).
func (h *NotificationsHandler) GetProjectMute(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleViewer) {
		return
	}
	mutes, err := h.DB.ListProjectNotificationMutes(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read mute state: "+err.Error())
		return
	}
	out := map[string]any{"muted": false}
	for _, m := range mutes {
		if m.Project == project {
			out = map[string]any{"muted": true, "since": m.CreatedAt, "by": m.CreatedBy}
			break
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// MuteProject silences external channel delivery for one project.
// Idempotent.
func (h *NotificationsHandler) MuteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if h.ProjectExists != nil {
		exists, err := h.ProjectExists(ctx, project)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "verify project: "+err.Error())
			return
		}
		if !exists {
			writeErr(w, http.StatusNotFound, "no such project")
			return
		}
	}
	by := ""
	if claims, ok := auth.ClaimsFromContext(ctx); ok {
		by = claims.UserID
	}
	if err := h.DB.SetProjectNotificationMute(ctx, project, by); err != nil {
		writeErr(w, http.StatusInternalServerError, "mute project: "+err.Error())
		return
	}
	if h.Notify != nil {
		h.Notify.InvalidateNotifications()
	}
	w.WriteHeader(http.StatusNoContent)
}

// UnmuteProject re-enables external channel delivery. Idempotent.
func (h *NotificationsHandler) UnmuteProject(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	project := chi.URLParam(r, "project")
	if !requireProjectAccess(ctx, w, h.DB, project, db.ProjectRoleEditor) {
		return
	}
	if err := h.DB.ClearProjectNotificationMute(ctx, project); err != nil {
		writeErr(w, http.StatusInternalServerError, "unmute project: "+err.Error())
		return
	}
	if h.Notify != nil {
		h.Notify.InvalidateNotifications()
	}
	w.WriteHeader(http.StatusNoContent)
}

// MyFeed returns recent NotificationEvent rows scoped to the
// caller's project memberships. Read-only; no unread / read-all /
// clear — those rely on a global readAt flag that would leak admin
// clicks to viewers and vice-versa. Admins still get the full
// /feed; this is the non-admin path.
func (h *NotificationsHandler) MyFeed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	claims, ok := auth.ClaimsFromContext(ctx)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	// Admins see everything via this path too — they may want to
	// embed the bell on a non-admin route, and there's no reason to
	// hide events from them.
	if auth.Has(claims.Permissions, auth.PermSettingsAdmin) {
		out, err := h.DB.ListNotificationEvents(ctx, limit, false)
		if err != nil {
			h.fail(w, "my-feed", err)
			return
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	tenancy, err := h.DB.ListUserTenancyCached(ctx, claims.UserID)
	if err != nil {
		h.fail(w, "my-feed: tenancy", err)
		return
	}
	projects := auth.ProjectsAccessible(tenancy)
	if len(projects) == 0 {
		writeJSON(w, http.StatusOK, []db.NotificationEvent{})
		return
	}
	out, err := h.DB.ListNotificationEventsForProjects(ctx, limit, projects)
	if err != nil {
		h.fail(w, "my-feed: list", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// Feed returns the most recent notification events. ?limit=N (clamp
// to 200) and ?unread=true narrow the result.
//
// Admin-only by design. The feed surfaces instance-wide events
// (deploy outcomes across every project, node health, backup
// failures) — it's the operator's pager view, not a per-user inbox.
// Per-event ACLs would need a different table shape (per-user
// readAt instead of the global readAt the schema carries today),
// so a future "scoped feed for project members" is a new endpoint,
// not a relaxation of this gate. The UI hides the bell when the
// caller doesn't have settings:admin so non-admins don't see a
// dead control.
func (h *NotificationsHandler) Feed(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	limit := 50
	if q := r.URL.Query().Get("limit"); q != "" {
		if n, err := strconv.Atoi(q); err == nil && n > 0 {
			limit = n
		}
	}
	unread := r.URL.Query().Get("unread") == "true"
	out, err := h.DB.ListNotificationEvents(ctx, limit, unread)
	if err != nil {
		h.fail(w, "feed", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// FeedUnread is the cheap counter the bell badge polls.
func (h *NotificationsHandler) FeedUnread(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	n, err := h.DB.CountUnreadNotificationEvents(ctx)
	if err != nil {
		h.fail(w, "unread count", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"unread": n})
}

// FeedReadAll stamps readAt on every unread event. Called when the
// user opens the bell popover.
func (h *NotificationsHandler) FeedReadAll(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.MarkAllNotificationEventsRead(ctx); err != nil {
		h.fail(w, "mark read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// FeedClear deletes every event in the in-app feed. Backs the
// "Clear" button in the bell popover. The notify.Dispatcher's
// in-memory channel is independent of this table — webhook
// fan-out for in-flight events is unaffected.
func (h *NotificationsHandler) FeedClear(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	n, err := h.DB.ClearAllNotificationEvents(ctx)
	if err != nil {
		h.fail(w, "clear feed", err)
		return
	}
	if h.Logger != nil {
		h.Logger.Info("notifications feed cleared", "rows", n, "user", auditUser(ctx))
	}
	w.WriteHeader(http.StatusNoContent)
}

// OutboxStats returns the pending + dead-letter row counts for the
// webhook delivery queue. The bell-icon feed bypasses the outbox, so
// these numbers only reflect external webhook health (Slack, Discord,
// generic webhook URLs). A non-zero `dead` means at least one channel
// is permanently misconfigured — the UI badges the card red.
func (h *NotificationsHandler) OutboxStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	pending, err := h.DB.CountOutboxPending(ctx)
	if err != nil {
		h.fail(w, "outbox pending", err)
		return
	}
	dead, err := h.DB.CountOutboxDead(ctx)
	if err != nil {
		h.fail(w, "outbox dead", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"pending": pending, "dead": dead})
}

// Test sends a synthetic event to the chosen notification config so
// the user can verify their Discord webhook URL works without having
// to wait for a real build to fire. Read the config, push one
// EventEnvelope onto the notify dispatcher, return 204.
func (h *NotificationsHandler) Test(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	n, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	if h.Notify == nil {
		writeErr(w, http.StatusServiceUnavailable, "notify dispatcher not wired")
		return
	}
	// Test sends bypass the event-whitelist filter — otherwise a
	// notification that doesn't have `test.ping` in its events list
	// (i.e. every real-world config) would silently drop the test.
	// SendDirect targets ONE notification, ignoring filters.
	if err := h.Notify.SendDirect(ctx, n, notify.Event{
		Type:     notify.EventTestPing,
		Title:    fmt.Sprintf("Test from kuso (%s)", n.Name),
		Body:     "If you can read this, your notification channel is wired up correctly.",
		Severity: "info",
	}); err != nil {
		h.Logger.Error("notify test", "name", n.Name, "err", err)
		writeErr(w, http.StatusBadGateway, "test send failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func notifCtx(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 5*time.Second)
}

func (h *NotificationsHandler) List(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	out, err := h.DB.ListNotifications(ctx)
	if err != nil {
		h.fail(w, "list", err)
		return
	}
	if out == nil {
		out = []db.Notification{}
	}
	for i := range out {
		out[i] = maskNotificationConfig(out[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": out})
}

func (h *NotificationsHandler) Get(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	out, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	masked := maskNotificationConfig(*out)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": masked})
}

type notifBody struct {
	Name      string         `json:"name"`
	Enabled   bool           `json:"enabled"`
	Type      string         `json:"type"`
	Pipelines []string       `json:"pipelines"`
	Events    []string       `json:"events"`
	Config    map[string]any `json:"config"`
}

func (h *NotificationsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body notifBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if body.Name == "" || body.Type == "" {
		writeErr(w, http.StatusBadRequest, "name and type required")
		return
	}
	if !validNotificationType(body.Type) {
		writeErr(w, http.StatusBadRequest, "type must be "+notificationTypeList)
		return
	}
	// A sentinel on create has no stored value to fall back to —
	// storing it literally would make the mask the credential.
	if configHasMaskSentinel(body.Config) {
		writeErr(w, http.StatusBadRequest, "config contains masked placeholder values — supply the real credential")
		return
	}
	if err := validateNotificationConfig(body.Type, body.Config); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, err := randomID()
	if err != nil {
		h.fail(w, "id", err)
		return
	}
	n := &db.Notification{
		ID: id, Name: body.Name, Enabled: body.Enabled, Type: body.Type,
		Pipelines: body.Pipelines, Events: body.Events, Config: body.Config,
	}
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.CreateNotification(ctx, n); err != nil {
		h.fail(w, "create", err)
		return
	}
	if h.Notify != nil {
		h.Notify.InvalidateNotifications()
	}
	masked := maskNotificationConfig(*n)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "data": masked, "message": "Notification created successfully"})
}

func (h *NotificationsHandler) Update(w http.ResponseWriter, r *http.Request) {
	var body notifBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request: "+err.Error())
		return
	}
	if body.Type != "" && !validNotificationType(body.Type) {
		writeErr(w, http.StatusBadRequest, "type must be "+notificationTypeList)
		return
	}
	ctx, cancel := notifCtx(r)
	defer cancel()
	existing, err := h.DB.FindNotification(ctx, chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, "find", err)
		return
	}
	if body.Config != nil {
		// GET returns credential fields masked, so a read-modify-write
		// from the UI echoes the sentinel back. Resolve sentinels to
		// the stored values BEFORE validating — otherwise saving an
		// unrelated field clobbers the real credential with the mask
		// (or fails URL validation on the sentinel string).
		body.Config = resolveMaskedConfig(body.Config, existing.Config)
	}
	// Validate the EFFECTIVE (type, config) pair — the pair that will be
	// stored — regardless of which of the two fields the body carries. A
	// PUT with only `type` used to skip validation entirely, switching
	// the channel type over a stored config that was never validated for
	// it (e.g. telegram → webhook reusing an un-SSRF-checked URL field).
	if err := validateNotificationConfig(
		effectiveNotifType(&body, existing),
		effectiveNotifConfig(&body, existing),
	); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Apply partial: only overwrite supplied fields.
	if body.Name != "" {
		existing.Name = body.Name
	}
	existing.Enabled = body.Enabled
	if body.Type != "" {
		existing.Type = body.Type
	}
	if body.Pipelines != nil {
		existing.Pipelines = body.Pipelines
	}
	if body.Events != nil {
		existing.Events = body.Events
	}
	if body.Config != nil {
		existing.Config = body.Config
	}
	if err := h.DB.UpdateNotification(ctx, existing); err != nil {
		h.fail(w, "update", err)
		return
	}
	if h.Notify != nil {
		h.Notify.InvalidateNotifications()
	}
	masked := maskNotificationConfig(*existing)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "data": masked, "message": "Notification updated successfully"})
}

func (h *NotificationsHandler) Delete(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := notifCtx(r)
	defer cancel()
	if err := h.DB.DeleteNotification(ctx, chi.URLParam(r, "id")); err != nil {
		h.fail(w, "delete", err)
		return
	}
	if h.Notify != nil {
		h.Notify.InvalidateNotifications()
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Notification deleted successfully"})
}

func (h *NotificationsHandler) fail(w http.ResponseWriter, op string, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeErr(w, http.StatusNotFound, notFoundMsg(err, db.ErrNotFound, kindFromOp(op)))
	default:
		h.Logger.Error("notifications handler", "op", op, "err", err)
		writeErr(w, http.StatusInternalServerError, "internal")
	}
}

func validNotificationType(t string) bool {
	switch t {
	case "slack", "webhook", "discord", "mattermost", "telegram", "pushover", "email":
		return true
	}
	return false
}

// notificationTypeList is the human-readable type list used in the
// 400 error message — kept in sync with validNotificationType.
const notificationTypeList = "slack, webhook, discord, mattermost, telegram, pushover, or email"

// validateNotificationConfig checks the per-type required config keys.
// URL-bearing channels (discord, webhook, slack, mattermost) get the
// SSRF guard; the API-token channels (telegram, pushover) post to
// fixed vendor hosts so they need no URL check; email validates the
// SMTP coordinates.
func validateNotificationConfig(typ string, cfg map[string]any) error {
	if cfg == nil {
		return errors.New("config required")
	}
	str := func(k string) string { s, _ := cfg[k].(string); return s }

	switch typ {
	case "discord", "webhook", "slack", "mattermost":
		rawURL := str("url")
		if rawURL == "" {
			return errors.New("config.url required")
		}
		if err := validateWebhookURL(rawURL); err != nil {
			return fmt.Errorf("config.url: %w", err)
		}
	case "telegram":
		if str("botToken") == "" || str("chatId") == "" {
			return errors.New("telegram notifications require config.botToken and config.chatId")
		}
	case "pushover":
		if str("token") == "" || str("user") == "" {
			return errors.New("pushover notifications require config.token and config.user")
		}
	case "email":
		if str("host") == "" || str("from") == "" || str("to") == "" {
			return errors.New("email notifications require config.host, config.from and config.to")
		}
	default:
		return fmt.Errorf("unsupported notification type %q", typ)
	}
	return nil
}

// isSecretNotifConfigKey reports whether a config key carries a
// credential for the given channel type. Webhook URLs count: Slack /
// Discord / Mattermost webhook URLs embed the secret in the path, so
// the URL IS the credential. The generic match is deliberately
// conservative — anything credential-shaped (token/password/secret/
// key/auth) masks, because a leaked mask on a benign field costs a
// re-type while a missed credential field is a HIGH.
func isSecretNotifConfigKey(typ, key string) bool {
	k := strings.ToLower(key)
	switch typ {
	case "discord", "webhook", "slack", "mattermost":
		if k == "url" || strings.HasSuffix(k, "url") {
			return true
		}
	}
	for _, marker := range []string{"token", "password", "secret", "apikey", "api_key", "credential", "auth"} {
		if strings.Contains(k, marker) {
			return true
		}
	}
	return false
}

// maskNotificationConfig returns a copy with credential-bearing config
// values replaced by envMaskSentinel — the same sentinel the env-var
// masking uses, so the UI's read-modify-write contract is uniform.
// The map is copied; the caller's (and any cache's) view is untouched.
func maskNotificationConfig(n db.Notification) db.Notification {
	if n.Config == nil {
		return n
	}
	cfg := make(map[string]any, len(n.Config))
	for k, v := range n.Config {
		if s, ok := v.(string); ok && s != "" && isSecretNotifConfigKey(n.Type, k) {
			cfg[k] = envMaskSentinel
			continue
		}
		cfg[k] = v
	}
	n.Config = cfg
	return n
}

// resolveMaskedConfig replaces sentinel values in an incoming config
// with the stored values, so the mask a client read back never
// overwrites a real credential. A sentinel with no stored counterpart
// is dropped — validation then reports the missing required field
// instead of the literal mask becoming the credential.
func resolveMaskedConfig(incoming, stored map[string]any) map[string]any {
	out := make(map[string]any, len(incoming))
	for k, v := range incoming {
		if s, ok := v.(string); ok && s == envMaskSentinel {
			if prev, ok := stored[k]; ok {
				out[k] = prev
			}
			continue
		}
		out[k] = v
	}
	return out
}

// effectiveNotifType returns the channel type an update will store:
// the body's when supplied, else the existing one. Mirrors the partial-
// update application below so validation always sees the stored pair.
func effectiveNotifType(body *notifBody, existing *db.Notification) string {
	if body.Type != "" {
		return body.Type
	}
	return existing.Type
}

// effectiveNotifConfig returns the config an update will store: the
// body's (sentinel-resolved) when supplied, else the existing one.
func effectiveNotifConfig(body *notifBody, existing *db.Notification) map[string]any {
	if body.Config != nil {
		return body.Config
	}
	return existing.Config
}

// configHasMaskSentinel reports whether any config value is the mask
// sentinel — only meaningful on create, where there's nothing stored
// to resolve it against.
func configHasMaskSentinel(cfg map[string]any) bool {
	for _, v := range cfg {
		if s, ok := v.(string); ok && s == envMaskSentinel {
			return true
		}
	}
	return false
}

// validateWebhookURL guards against SSRF-via-notification: an
// admin-only flow, but Test sends straight from the kuso server
// at-will, so a URL pointing at 169.254.169.254 (cloud IMDS),
// 10.x cluster internals, or http://kuso-postgres-conn-thing.kuso.svc
// would let an admin who *should* only have HTTP-out reach kube
// internals. Cheap allowlist:
//   - scheme must be http or https
//   - host must parse as a non-empty hostname
//   - host must not be an IP literal in a private/loopback/link-
//     local range, or a *.svc / *.cluster.local DNS suffix.
//
// We deliberately don't resolve DNS at validation time (race with
// "DNS rebinding" + the lookup happens later anyway when notify
// dispatches). The dispatcher should also enforce an SSRF-safe
// dialer in a follow-up.
func validateWebhookURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("scheme must be http or https")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("missing host")
	}
	hostLower := strings.ToLower(host)
	if hostLower == "localhost" {
		return errors.New("localhost is not allowed")
	}
	for _, suf := range []string{".svc", ".svc.cluster.local", ".cluster.local", ".internal", ".local"} {
		if strings.HasSuffix(hostLower, suf) {
			return fmt.Errorf("cluster-internal hostnames (%s) are not allowed", suf)
		}
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
			ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("IP %s is in a reserved/private range", ip)
		}
	}
	return nil
}
