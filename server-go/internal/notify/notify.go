// Package notify is the event fan-out for kuso. Domain code emits
// events ("build.succeeded", "pod.crashed", etc.); the dispatcher
// reads notification configs from the DB and pushes formatted
// payloads to every enabled sink (Discord webhook, generic webhook,
// Slack later).
//
// Design constraints:
//   - Durable: Emit writes the bell-feed row and one outbox row per
//     matching channel synchronously (bounded by a short timeout), so
//     an event survives a restart right after it's emitted. Domain code
//     never waits on a slow webhook — the outbox workers deliver.
//   - Per-event filtering: the DB row carries an `events` whitelist;
//     empty list = all events. Rows can be disabled without deletion.
//   - Per-project filtering: the `pipelines` column is a project
//     whitelist; empty = all projects. Project-less events always pass.
//
// The dispatcher is safe to call from anywhere: missing DB or
// nil dispatcher is a no-op.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/db"
	"kuso/server/internal/failures"
	"kuso/server/internal/httpx"
)

// EventType is one of a fixed set so consumers can filter cleanly.
type EventType string

const (
	// EventBuildStarted and EventDeployRolled are never emitted. The
	// consts stay so stored channel configs that list them still parse;
	// they are deliberately absent from AllEventTypes / EventCatalogue.
	EventBuildStarted    EventType = "build.started"
	EventBuildSucceeded  EventType = "build.succeeded"
	EventBuildFailed     EventType = "build.failed"
	EventBuildCancelled  EventType = "build.cancelled"
	EventBuildSuperseded EventType = "build.superseded"
	EventDeployRolled    EventType = "deploy.rolled"
	EventPodCrashed      EventType = "pod.crashed"
	EventAddonCrashed    EventType = "addon.crashed"
	// EventPodRecovered closes a pod.crashed episode: the service's env
	// has been healthy for a stability window after crashing.
	EventPodRecovered EventType = "pod.recovered"
	EventAlertFired      EventType = "alert.fired"
	EventBackupOK        EventType = "backup.succeeded"
	EventBackupFailed    EventType = "backup.failed"
	// Node lifecycle events. Fired by the nodewatch goroutine when a
	// kube node has been NotReady past the watcher's threshold.
	// Recovery emits EventNodeRecovered so the operator sees both
	// edges of the outage.
	EventNodeUnreachable EventType = "node.unreachable"
	EventNodeRecovered   EventType = "node.recovered"
	// Host package-update advisory. Fired by the pkgupdates watcher when
	// a node gains a fresh advisory (the probe DaemonSet found available
	// host-OS package updates). Informational (warn severity, no @here);
	// the operator applies patches from the nodes page.
	EventNodeUpdatesAvailable EventType = "node.updates-available"
	// EventNodeUpdatesApplied fires when an apply-updates run (patch +
	// optional reboot) finishes on a node.
	EventNodeUpdatesApplied EventType = "node.updates-applied"
	// Run lifecycle events. Fired when a KusoRun is created (started)
	// and when the runs poller observes terminal phase transitions
	// (succeeded / failed). A failed migration on prod is exactly the
	// kind of event that should ping #incidents — separately from
	// build / deploy events so operators can subscribe granularly.
	EventRunStarted   EventType = "run.started"
	EventRunSucceeded EventType = "run.succeeded"
	EventRunFailed    EventType = "run.failed"
	// Cron lifecycle events. Fired by the cronwatch goroutine when a
	// scheduled KusoCron Job exits non-zero. Separate from the run/
	// build events because crons fail silently into the deployments
	// tab by default — refund-deadline sweeps, voucher expiry, etc.
	// need to ping an oncall channel directly.
	EventCronFailed EventType = "cron.failed"
	// Diagnostic ping fired from the "send a test message" button on
	// the notification settings page. Not a real platform event —
	// don't subscribe to it from alerts.
	EventTestPing EventType = "test.ping"
)

// EventTypeInfo is one entry of the subscribable-event catalogue served
// at GET /api/notifications/event-types. The JSON shape is a contract
// with the web settings UI.
type EventTypeInfo struct {
	Type  EventType `json:"type"`
	Label string    `json:"label"`
	// Group is one of build | runtime | jobs | nodes | backups | other.
	Group string `json:"group"`
	// DefaultMention is what mentionFor falls back to when a channel has
	// no explicit rule: "@here" for types emitted at error severity,
	// "" otherwise.
	DefaultMention string `json:"defaultMention"`
}

// EventCatalogue lists every event type a channel can subscribe to, in
// display order. DefaultMention mirrors the severity each emit site
// uses. Mixed-severity types take the common case:
//   - build.cancelled: info (lifecycle), warn from promotion-group aborts.
//   - alert.fired: the rule's own severity (API default warn); node
//     disk-pressure alerts are warn. error-severity rules still @here.
//   - backup.failed: error when a configured backup broke, warn when
//     backups were never configured.
var EventCatalogue = []EventTypeInfo{
	{EventBuildSucceeded, "Build succeeded", "build", ""},
	{EventBuildFailed, "Build failed", "build", "@here"}, // production only; other envs are warn
	{EventBuildCancelled, "Build cancelled", "build", ""},
	{EventBuildSuperseded, "Build superseded", "build", ""},
	{EventPodCrashed, "Pod crashed", "runtime", "@here"}, // production only; other envs are warn
	{EventPodRecovered, "Pod recovered", "runtime", ""},
	{EventAddonCrashed, "Addon crashed", "runtime", "@here"},
	{EventAlertFired, "Alert fired", "runtime", ""},
	{EventRunStarted, "Run started", "jobs", ""},
	{EventRunSucceeded, "Run succeeded", "jobs", ""},
	{EventRunFailed, "Run failed", "jobs", "@here"},
	{EventCronFailed, "Cron failed", "jobs", ""},
	{EventNodeUnreachable, "Node unreachable", "nodes", "@here"},
	{EventNodeRecovered, "Node recovered", "nodes", ""},
	{EventNodeUpdatesAvailable, "Host updates available", "nodes", ""},
	{EventNodeUpdatesApplied, "Host updates applied", "nodes", ""},
	{EventBackupOK, "Backup succeeded", "backups", ""},
	{EventBackupFailed, "Backup failed", "backups", "@here"},
}

// AllEventTypes is the canonical list of every event type the notify
// package emits: the catalogue plus test.ping (emitted by the Test
// button, not subscribable). Add new event types to the const block
// AND EventCatalogue.
var AllEventTypes = func() []EventType {
	out := make([]EventType, 0, len(EventCatalogue)+1)
	for _, c := range EventCatalogue {
		out = append(out, c.Type)
	}
	return append(out, EventTestPing)
}()

// Event is the wire-stable payload domain code emits. JSON-serialised
// straight to webhook sinks; rendered to embeds for Discord/Slack.
//
// Field shape grew over time. The original four (Title/Body/URL/Extra)
// produced the early "thin card" Discord embeds. The richer fields
// (Description/LogTail/Fields/Footer/DurationMs) feed the redesigned
// embeds — emit sites that don't populate them still get a clean
// (if less informative) card via the renderer's missing-field
// fall-throughs.
type Event struct {
	Type      EventType         `json:"type"`
	Timestamp time.Time         `json:"timestamp"`
	Project   string            `json:"project,omitempty"`
	Service   string            `json:"service,omitempty"`
	Title     string            `json:"title"`
	Body      string            `json:"body,omitempty"`
	URL       string            `json:"url,omitempty"`
	Severity  string            `json:"severity,omitempty"` // info | warn | error
	Extra     map[string]string `json:"extra,omitempty"`

	// Rich-card fields. All optional; the Discord renderer drops
	// missing ones gracefully so older emit sites that only set the
	// classic fields still produce a valid (if thinner) embed.

	// Description is short prose under the title — commit message,
	// crash reason, etc. One paragraph; the renderer truncates at
	// Discord's per-embed description limit (4096 chars).
	Description string `json:"description,omitempty"`
	// LogTail is the tail of relevant logs (last ~5 lines is the
	// expected shape). Renderer wraps it in a code fence; long tails
	// move from the description into a dedicated full-width field
	// because Discord caps description at 4096 but a field value at
	// 1024 — the split is a renderer concern, not the caller's.
	LogTail string `json:"logTail,omitempty"`
	// DurationMs is wall-clock duration for events that have one
	// (build start→finish, deploy roll). Renderer formats as "1m 24s"
	// or "12s".
	DurationMs int64 `json:"durationMs,omitempty"`
	// Fields is the inline field block under the description. Order
	// preserved. Mixing inline=true and inline=false works the way
	// Discord renders it: inlines pack 3-up; non-inline forces a row.
	Fields []EventField `json:"fields,omitempty"`
	// Footer is an optional override for the default footer (the
	// instance host). Empty = use default.
	Footer string `json:"footer,omitempty"`
	// Env is the environment group the event concerns (production,
	// staging, preview-pr-7, …); "" for events not tied to one.
	Env string `json:"env,omitempty"`
	// Links is the card's action row (Logs · Deployments · Site),
	// rendered last. See EventLink.
	Links []EventLink `json:"links,omitempty"`
	// Classification, when populated on failure events, tells the web
	// UI which overlay tab to open + which log line to highlight when
	// the user clicks the bell-popover row. nil for non-failure events
	// and for legacy emit sites that don't populate it yet — the UI
	// falls back to "open the service page". See internal/failures
	// for the kind taxonomy + per-kind summary text.
	Classification *failures.Classification `json:"classification,omitempty"`
}

// EventField is one row in the rich-card 2-column field block. Mirrors
// the Discord embed field shape so the renderer can map 1:1; webhook
// sinks see the same JSON.
type EventField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// Dispatcher is the fan-out service. Construct via New + start with
// Run in a goroutine.
type Dispatcher struct {
	db     *db.DB
	logger *slog.Logger
	client *http.Client

	// lookupChannelFn is a test seam for drainOne's channel lookup;
	// nil = d.lookupNotification.
	lookupChannelFn func(ctx context.Context, id string) (db.Notification, error)

	// isLeader, when set, gates the outbox DRAIN (outboxWorker →
	// shouldRunOutbox) so multi-replica installs don't N-times-deliver
	// the same event to Slack/Discord. It does NOT gate outbox ENQUEUE
	// (Emit) — any replica must be able to persist an emitted event to
	// the durable outbox, or events from a pod that isn't the singletons
	// leader are lost. Delivery stays single-flight via the leader-gated
	// drain + FOR UPDATE SKIP LOCKED claim. The bell-icon feed (Emit's
	// persist) dedups via a DB unique constraint. nil = always-on
	// (single-replica installs and tests).
	isLeader func() bool

	// eventHook, when set, is called synchronously from Emit AFTER the
	// event is persisted, on the leader only. The incidents.Manager
	// registers one to react to pod.crashed / alert.fired /
	// node.unreachable. It must be cheap + non-blocking (it spawns work
	// async); a slow hook would stall the Emit caller. nil = no hook.
	eventHook func(Event)

	mu     sync.Mutex
	closed bool

	// baseCtx is the dispatcher's lifecycle context, set by Run.
	// Emit's synchronous persist derives from this so a graceful
	// shutdown actually cancels in-flight SQLite writes (the previous
	// context.Background() left them running against a closing DB).
	// Seeded to Background in New so Emit calls before Run still
	// have a usable parent.
	baseCtx context.Context

	// notifsCache is a short-lived cache of the configured notification
	// channels. Without it, every emitted event does a fresh
	// SQLite SELECT + JSON decode — which on a build storm + a single-
	// connection writer pool starves every other writer (audit log,
	// nodemetrics insert, login). Cache lives for notifsCacheTTL and
	// is invalidated explicitly when the notifications handler does a
	// CREATE/UPDATE/DELETE so admins see their config changes apply
	// to the next event without waiting for the TTL.
	notifsMu      sync.RWMutex
	notifsCache   []db.Notification
	notifsExpires time.Time

	// mutedCache mirrors notifsCache for the per-project mute set: the
	// Emit hot path checks it before enqueueing outbox rows, so it
	// must not hit the DB per event. Same TTL + invalidation as the
	// channel configs (the mute handler calls InvalidateNotifications).
	mutedMu      sync.RWMutex
	mutedCache   map[string]bool
	mutedExpires time.Time
}

// notifsCacheTTL bounds how stale the dispatcher's view of the
// notifications table can be. 30s matches the bell-icon polling
// cadence and is short enough that a misconfigured channel can be
// disabled without restarting the server.
const notifsCacheTTL = 30 * time.Second

// SetLeaderHook installs a predicate that gates webhook fan-out.
// Call once during boot, after leader election is wired. Pass nil to
// reset to always-on behaviour (e.g. in tests).
func (d *Dispatcher) SetLeaderHook(fn func() bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.isLeader = fn
	d.mu.Unlock()
}

// SetEventHook installs a callback invoked (leader-only, after persist)
// for every emitted event. The incidents.Manager uses it to open/attach
// incidents. The hook must return quickly — it runs on the Emit path.
// Pass nil to clear.
func (d *Dispatcher) SetEventHook(fn func(Event)) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.eventHook = fn
	d.mu.Unlock()
}

// New returns a dispatcher bound to a DB for config lookup. queueSize
// is ignored: it sized the old in-memory dispatch channel, which is no
// longer on the delivery path. Kept so existing callers compile.
func New(database *db.DB, logger *slog.Logger, queueSize int) *Dispatcher {
	_ = queueSize
	return &Dispatcher{
		db:      database,
		logger:  logger,
		baseCtx: context.Background(),
		// SSRF-safe, redirect-refusing client. The dial guard rejects
		// link-local, loopback, and private (RFC1918 + RFC4193)
		// ranges — a user with notification:write could otherwise
		// point a webhook at 169.254.169.254 (cloud metadata) or
		// 10.0.0.0/8 (in-cluster apiserver / addon DBs). Redirects
		// are refused outright: a 302 hop gets its own DNS
		// resolution, and webhook POST delivery has no legitimate
		// redirect use.
		client: httpx.SSRFSafeNoRedirectClient(8 * time.Second),
	}
}

// (isReservedIP / isAllowPrivateIPs / blockCIDRs moved to
// kuso/server/internal/httpx. Both notify and the Coolify importer
// share the same dialer guard. See httpx/ssrf.go for the policy.)

// emitWriteTimeout bounds each of Emit's synchronous DB steps (feed
// persist, outbox enqueue) so a wedged DB can't stall the caller.
const emitWriteTimeout = 2 * time.Second

// Emit persists the event to the in-app feed and enqueues one outbox row
// per matching channel, both synchronously. There is no in-memory queue
// on the delivery path: once Emit returns, the event survives a restart
// (e.g. a self-update roll) and delivery is up to the outbox workers.
// Each DB step is bounded by emitWriteTimeout; failures are logged and
// counted, never returned — domain code must not branch on notify.
func (d *Dispatcher) Emit(e Event) {
	if d == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	e.Severity = normalizeSeverity(e.Severity)
	d.mu.Lock()
	closed := d.closed
	parent := d.baseCtx
	hook := d.eventHook
	leader := d.isLeader
	d.mu.Unlock()
	if closed {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	metricsEmitted.WithLabelValues(string(e.Type)).Inc()
	if d.db != nil {
		d.persistFeed(parent, e)
		d.enqueueOutbox(parent, e)
	}
	// Event hook (incidents.Manager): leader-only, after persist. Called
	// outside d.mu; the hook spawns async work and must not block.
	if hook != nil && (leader == nil || leader()) {
		hook(e)
	}
}

// persistFeed writes the bell-icon NotificationEvent row. Parented on
// the dispatcher's lifecycle ctx so a graceful shutdown cancels it.
func (d *Dispatcher) persistFeed(parent context.Context, e Event) {
	ctx, cancel := context.WithTimeout(parent, emitWriteTimeout)
	defer cancel()
	// Serialise the classification (if any) so the bell-icon list
	// endpoint can hand the raw JSON to the browser without re-encoding
	// on every read. nil stays NULL in DB and omitempty on the wire.
	var classification json.RawMessage
	if e.Classification != nil {
		if b, mErr := json.Marshal(e.Classification); mErr == nil {
			classification = b
		}
	}
	if err := d.db.InsertNotificationEvent(ctx, db.NotificationEvent{
		Type:           string(e.Type),
		Title:          e.Title,
		Body:           e.Body,
		Severity:       e.Severity,
		Project:        e.Project,
		Service:        e.Service,
		URL:            e.URL,
		Extra:          e.Extra,
		Classification: classification,
	}); err != nil && d.logger != nil {
		d.logger.Warn("notify: persist event", "err", err, "type", string(e.Type))
	}
}

// normalizeSeverity folds severity spellings into the three values every
// renderer + the mention default switch on (info | warn | error). Emit
// sites wrote "warning", which matched none of them: no warn colour, and
// release-hook failures rendered as neutral navy cards.
func normalizeSeverity(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "warn", "warning":
		return "warn"
	case "error", "err", "critical", "fatal":
		return "error"
	default:
		return "info"
	}
}

// Run binds the dispatcher's lifecycle to ctx: Emit's DB writes are
// parented on it, and once it's canceled Emit becomes a no-op. Blocks
// until ctx is done; call once in a background goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	d.mu.Lock()
	d.baseCtx = ctx
	d.mu.Unlock()
	<-ctx.Done()
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
}

// enqueueOutbox writes one outbox row per channel that should receive
// e. The outbox drain workers (outbox.go) deliver them.
//
// Intentionally NOT leader-gated. Any replica that emits an event must
// be able to write it to the durable outbox — otherwise events emitted
// on a pod that doesn't hold the "singletons" lease are silently
// dropped. Single delivery is enforced downstream: the drain workers are
// leader-gated and ClaimOutboxRow uses FOR UPDATE SKIP LOCKED + a lease.
func (d *Dispatcher) enqueueOutbox(parent context.Context, e Event) {
	ctx, cancel := context.WithTimeout(parent, emitWriteTimeout)
	defer cancel()
	notifs, err := d.cachedNotifications(ctx)
	if err != nil {
		metricsDropped.Inc()
		d.logger.Warn("notify: list configs, webhook fanout skipped", "err", err, "type", string(e.Type))
		return
	}
	// Per-project mute: muted projects skip external channel delivery
	// entirely. The bell feed (persistFeed) is untouched, so the in-app
	// audit trail survives a mute. Project-less events (node.*, backup
	// health) are never muted. Fail-open: if the mute read errors,
	// deliver — a missed mute beats silently dropped notifications.
	//
	// Error-severity alert.fired bypasses the mute: mute exists to
	// silence deploy chatter, but an alert rule the team explicitly
	// marked severity=error is a page ("service down"), and a mute
	// that swallows pages turns "stop pinging us" into "never learn
	// prod is down". info/warn alerts stay muted with everything else.
	pageThrough := e.Type == EventAlertFired && e.Severity == "error"
	if e.Project != "" && !pageThrough {
		if muted, merr := d.cachedMutedProjects(ctx); merr != nil {
			d.logger.Warn("notify: list muted projects", "err", merr)
		} else if muted[e.Project] {
			return
		}
	}
	var payload []byte
	for _, n := range notifs {
		if !n.Enabled || !deliverableChannel(n.Type) {
			continue
		}
		if !eventMatches(string(e.Type), n.Events) || !projectMatches(e.Project, n.Pipelines) {
			continue
		}
		if payload == nil {
			p, perr := db.MarshalOutboxPayload(e)
			if perr != nil {
				metricsDropped.Inc()
				d.logger.Warn("notify: marshal outbox payload", "err", perr, "type", string(e.Type))
				return
			}
			payload = p
		}
		if _, err := d.db.EnqueueOutbox(ctx, n.ID, string(e.Type), payload); err != nil {
			metricsDropped.Inc()
			d.logger.Warn("notify: enqueue outbox", "err", err, "channel", n.ID, "type", string(e.Type))
			continue
		}
		metricsEnqueued.WithLabelValues(string(e.Type)).Inc()
	}
}

// SendDirect fires a single event at exactly one notification config,
// synchronously, bypassing the event whitelist + the async queue.
// Used by the Test endpoint so users get an actual HTTP error back
// when their webhook URL is wrong / their Discord channel was deleted
// / the secret got rotated. The async path swallows those errors.
func (d *Dispatcher) SendDirect(ctx context.Context, n *db.Notification, e Event) error {
	if n == nil {
		return fmt.Errorf("notification is nil")
	}
	if !n.Enabled {
		return fmt.Errorf("channel %q is disabled", n.Name)
	}
	// Delegate to deliverViaChannel so the Test path and the outbox
	// retry path render + send identically — one switch, no drift.
	return d.deliverViaChannel(ctx, *n, e)
}

// cachedNotifications returns the dispatcher's view of the configured
// notification channels, refreshing from SQLite on cache miss. Cache
// hits are read-locked so high-frequency event bursts (build storms,
// alert flurries) all walk an in-memory slice instead of contending
// for the single-writer DB connection.
func (d *Dispatcher) cachedNotifications(ctx context.Context) ([]db.Notification, error) {
	d.notifsMu.RLock()
	if time.Now().Before(d.notifsExpires) && d.notifsCache != nil {
		out := d.notifsCache
		d.notifsMu.RUnlock()
		return out, nil
	}
	d.notifsMu.RUnlock()

	// Cache miss / expired. Take the write lock for the refresh so
	// concurrent dispatchers don't all hit SQLite at once.
	d.notifsMu.Lock()
	defer d.notifsMu.Unlock()
	// Double-check under the write lock — a sibling goroutine may have
	// refreshed while we were waiting.
	if time.Now().Before(d.notifsExpires) && d.notifsCache != nil {
		return d.notifsCache, nil
	}
	notifs, err := d.db.ListNotifications(ctx)
	if err != nil {
		return nil, err
	}
	if notifs == nil {
		// Non-nil so "no channels configured" is a cache hit too; Emit
		// reads this synchronously on every event.
		notifs = []db.Notification{}
	}
	d.notifsCache = notifs
	d.notifsExpires = time.Now().Add(notifsCacheTTL)
	return notifs, nil
}

// InvalidateNotifications drops the cached config slice so the next
// event re-reads from SQLite. Called from the notifications handler
// on every CREATE / UPDATE / DELETE so admins don't see an apparent
// "the channel is enabled but events aren't going through" lag while
// the cache TTL ages out.
func (d *Dispatcher) InvalidateNotifications() {
	if d == nil {
		return
	}
	d.notifsMu.Lock()
	d.notifsCache = nil
	d.notifsExpires = time.Time{}
	d.notifsMu.Unlock()
	d.mutedMu.Lock()
	d.mutedCache = nil
	d.mutedExpires = time.Time{}
	d.mutedMu.Unlock()
}

// cachedMutedProjects returns the set of projects whose events skip
// external channel delivery, refreshing from the DB on cache miss.
// Mirrors cachedNotifications (same TTL, same locking discipline).
func (d *Dispatcher) cachedMutedProjects(ctx context.Context) (map[string]bool, error) {
	d.mutedMu.RLock()
	if time.Now().Before(d.mutedExpires) && d.mutedCache != nil {
		out := d.mutedCache
		d.mutedMu.RUnlock()
		return out, nil
	}
	d.mutedMu.RUnlock()

	d.mutedMu.Lock()
	defer d.mutedMu.Unlock()
	if time.Now().Before(d.mutedExpires) && d.mutedCache != nil {
		return d.mutedCache, nil
	}
	mutes, err := d.db.ListProjectNotificationMutes(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(mutes))
	for _, m := range mutes {
		set[m.Project] = true
	}
	d.mutedCache = set
	d.mutedExpires = time.Now().Add(notifsCacheTTL)
	return set, nil
}

// projectMatches reports whether a channel's project whitelist (the
// Notification.pipelines column) admits an event from project. Empty
// whitelist = all projects. Project-less events (node.*, backup health,
// test.ping) always pass: they're cluster-wide and a project filter
// can't meaningfully exclude them.
func projectMatches(project string, whitelist []string) bool {
	if project == "" || len(whitelist) == 0 {
		return true
	}
	for _, w := range whitelist {
		if w == project {
			return true
		}
	}
	return false
}

// eventMatches returns true if `event` is in `whitelist`, or if the
// whitelist is empty (= match all).
func eventMatches(event string, whitelist []string) bool {
	if len(whitelist) == 0 {
		return true
	}
	for _, w := range whitelist {
		if w == event {
			return true
		}
	}
	return false
}

// sendDiscord posts a Discord-formatted embed to the webhook URL.
// Discord rejects non-2xx silently from the sender's perspective, so
// we log on any non-2xx for debugging.
//
// mention is rendered as the message content (not the embed), so
// Discord renders @here / @everyone / <@&roleID> as actual pings
// at the top of the card.
func (d *Dispatcher) sendDiscord(ctx context.Context, url string, e Event, mention string) {
	d.post(ctx, url, discordPayload(e, mention), nil)
}

// mentionFor reads the per-event mention rule out of Config.mentions.
// Falls back to a "*" default if set, otherwise an opinionated
// default: any error-severity event without an explicit rule gets
// @here so an outage isn't silent. Set "*": "none" (or any non-
// mention string) to opt out of the default.
func mentionFor(e Event, config map[string]any) string {
	mentions, _ := config["mentions"].(map[string]any)
	if v, ok := mentions[string(e.Type)].(string); ok {
		return normalizeMention(v)
	}
	if v, ok := mentions["*"].(string); ok {
		return normalizeMention(v)
	}
	// No explicit rule — default error-severity events to @here.
	if e.Severity == "error" {
		return "@here"
	}
	return ""
}

// normalizeMention coerces UI-friendly strings to Discord wire form.
// "@here", "@everyone" pass through; "role:<id>" → "<@&id>"; "none"
// or empty → "" (no mention).
func normalizeMention(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "none" {
		return ""
	}
	if strings.HasPrefix(v, "role:") {
		return "<@&" + strings.TrimPrefix(v, "role:") + ">"
	}
	return v
}

// allowedMentionsFor builds the Discord allowed_mentions object that
// matches what we put in `content`. Webhooks with default settings
// strip @here / @everyone unless we explicitly whitelist them.
func allowedMentionsFor(mention string) map[string]any {
	parse := []string{}
	roles := []string{}
	switch {
	case strings.Contains(mention, "@everyone"):
		parse = append(parse, "everyone")
	case strings.Contains(mention, "@here"):
		parse = append(parse, "everyone") // Discord groups @here under "everyone"
	}
	// Role pings look like "<@&123>"; pull the IDs out for the
	// allowed_mentions.roles whitelist.
	for _, m := range roleMentionRE.FindAllStringSubmatch(mention, -1) {
		roles = append(roles, m[1])
	}
	out := map[string]any{"parse": parse}
	if len(roles) > 0 {
		out["roles"] = roles
	}
	return out
}

var roleMentionRE = regexp.MustCompile(`<@&(\d+)>`)

// tone is the at-a-glance colour class every renderer (Discord embed
// colour, Slack bar, text-channel emoji) derives from, so they agree.
type tone int

const (
	toneInfo tone = iota
	toneSuccess
	toneNeutral
	toneWarn
	toneError
)

// eventTone: severity wins (Emit normalizes it to info|warn|error, so
// every failure type arrives as error); info events are then split by
// type into success / neutral / plain info.
func eventTone(e Event) tone {
	switch e.Severity {
	case "error":
		return toneError
	case "warn":
		return toneWarn
	}
	switch e.Type {
	case EventBuildSucceeded, EventBackupOK, EventRunSucceeded,
		EventNodeRecovered, EventNodeUpdatesApplied, EventPodRecovered:
		return toneSuccess
	case EventBuildCancelled, EventBuildSuperseded:
		return toneNeutral
	}
	return toneInfo
}

func discordColor(e Event) int {
	switch eventTone(e) {
	case toneError:
		return 0xEF4444 // red
	case toneWarn:
		return 0xF59E0B // amber
	case toneSuccess:
		return 0x10B981 // emerald
	case toneNeutral:
		return 0x9CA3AF // grey
	default:
		return 0x40476D // navy (matches the logo)
	}
}

// discordEmbedMaxChars is Discord's cap on the summed characters of an
// embed's title, description, field names/values and footer text. Over
// it the whole webhook POST is rejected with a 400.
const discordEmbedMaxChars = 6000

// discordPayload assembles the full Discord webhook body for an Event.
// Shared by sendDiscord + sendDiscordSync so the two paths can't drift.
//
// Card grammar:
//
//	┌─ <glyph> <What> · <project> / <service> → <env>  (linked) ─┐
//	│ Description — commit message, crash summary, …            │
//	│ 💡 Diagnosis + fix (failures kuso recognises)              │
//	│ ```log tail```                                            │
//	│ Field A    Field B    Field C   (inline, 3-up)            │
//	│ Logs · Deployments · Site      (link row)                  │
//	│ <instance host>                                   <time>  │
//	└───────────────────────────────────────────────────────────┘
//
// Compact cards (successes, cancellations) fold the fields into one
// description line and skip the log tail, so failures stand out.
// Extra is NOT rendered: it's raw-webhook data (ids, internal keys),
// and cards showing "rule_id: r1" was noise. Anything a human should
// see goes in Fields.
func discordPayload(e Event, mention string) map[string]any {
	const m = markupDiscord
	embed := map[string]any{
		"title":     truncateRunes(e.Title, 256),
		"color":     discordColor(e),
		"timestamp": e.Timestamp.Format(time.RFC3339),
	}
	if abs := absoluteURL(e.URL); abs != "" {
		embed["url"] = abs
	}
	compact := isCompact(e)

	parts := make([]string, 0, 4)
	desc := cardDescription(e)
	if desc != "" {
		parts = append(parts, expandTimes(desc, m))
	}
	if compact {
		if line := inlineFields(e.Fields, m); line != "" {
			parts = append(parts, expandTimes(line, m))
		}
	}
	if diag := diagnosisText(e.Classification, m); diag != "" {
		parts = append(parts, diag)
	}
	// Log tail goes inline when it fits, else into a Logs field (field
	// values cap at 1024, description at 4096).
	logTail := strings.TrimSpace(e.LogTail)
	logInDesc := false
	if logTail != "" && !compact {
		fenced := "```\n" + logTail + "\n```"
		if len(strings.Join(parts, "\n"))+1+len(fenced) <= 3500 {
			parts = append(parts, fenced)
			logInDesc = true
		}
	}
	links := linksLine(e, m)
	body := strings.Join(parts, "\n")
	if links != "" {
		body = truncateRunes(body, 4096-len([]rune(links))-2)
		if body != "" {
			body += "\n\n"
		}
		body += links
	}
	if body != "" {
		embed["description"] = truncateRunes(body, 4096)
	}

	fields := make([]map[string]any, 0, len(e.Fields)+1)
	if !compact {
		for _, f := range e.Fields {
			if f.Name == "" || f.Value == "" {
				continue
			}
			fields = append(fields, map[string]any{
				"name":   truncateRunes(f.Name, 256),
				"value":  truncateRunes(expandTimes(f.Value, m), 1024),
				"inline": f.Inline,
			})
		}
		if logTail != "" && !logInDesc {
			fields = append(fields, map[string]any{
				"name":   "Logs",
				"value":  truncateRunes("```\n"+logTail+"\n```", 1024),
				"inline": false,
			})
		}
	}
	if len(fields) > 25 {
		fields = fields[:25]
	}
	if len(fields) > 0 {
		embed["fields"] = fields
	}
	footer := e.Footer
	if footer == "" {
		footer = instanceHost()
	}
	footer = truncateRunes(footer, 2048)
	if footer != "" {
		embed["footer"] = map[string]any{"text": footer}
	}
	fitDiscordEmbed(embed, footer, links)
	payload := map[string]any{
		"username": "kuso",
		"embeds":   []any{embed},
	}
	if av := avatarURL(); av != "" {
		payload["avatar_url"] = av
	}
	// content is what a phone's push notification shows. Without it the
	// push is the embed's raw markdown, and a ping reads just "@here".
	payload["content"] = truncateRunes(strings.TrimSpace(mention+" "+pushLine(e)), 2000)
	// Allowed_mentions explicitly enables the parsing — without this
	// Discord strips @here / @everyone for hardened webhooks. Roles need
	// explicit IDs in `roles`. With no mention it parses nothing, so a
	// title or reason containing "@everyone" can never ping.
	payload["allowed_mentions"] = allowedMentionsFor(mention)
	return payload
}

// fitDiscordEmbed trims an assembled embed until it's within
// discordEmbedMaxChars: description first, then the Logs field, then
// drops fields from the end. footer is the already-truncated footer text;
// keepSuffix (the link row) survives description trimming.
func fitDiscordEmbed(embed map[string]any, footer, keepSuffix string) {
	count := func(s any) int {
		str, _ := s.(string)
		return len([]rune(str))
	}
	fields, _ := embed["fields"].([]map[string]any)
	total := func() int {
		n := count(embed["title"]) + count(embed["description"]) + len([]rune(footer))
		for _, f := range fields {
			n += count(f["name"]) + count(f["value"])
		}
		return n
	}
	over := total() - discordEmbedMaxChars
	if over <= 0 {
		return
	}
	if d := count(embed["description"]); d > 0 {
		full := embed["description"].(string)
		head, tail := full, ""
		if keepSuffix != "" && strings.HasSuffix(full, keepSuffix) {
			head, tail = strings.TrimSuffix(full, keepSuffix), keepSuffix
		}
		keep := len([]rune(head)) - over
		switch {
		case keep > 0:
			embed["description"] = truncateRunes(head, keep) + tail
		case tail != "":
			embed["description"] = tail
		default:
			delete(embed, "description")
		}
		over = total() - discordEmbedMaxChars
	}
	for i := range fields {
		if over <= 0 || fields[i]["name"] != "Logs" {
			continue
		}
		v := fields[i]["value"].(string)
		keep := len([]rune(v)) - over
		if keep <= 0 {
			fields = append(fields[:i], fields[i+1:]...)
		} else {
			fields[i]["value"] = truncateRunes(v, keep)
		}
		over = total() - discordEmbedMaxChars
		break
	}
	for over > 0 && len(fields) > 0 {
		fields = fields[:len(fields)-1]
		over = total() - discordEmbedMaxChars
	}
	if over > 0 {
		// Title + footer alone can't exceed 256 + 2048; unreachable, but
		// keep the embed valid if the caps ever change.
		embed["title"] = truncateRunes(embed["title"].(string), count(embed["title"])-over)
	}
	if len(fields) == 0 {
		delete(embed, "fields")
	} else {
		embed["fields"] = fields
	}
}

// truncateRunes shortens s to at most max RUNES (not bytes), appending
// "…" when truncation actually happened. We count runes because Discord
// limits are in characters, and a byte-count truncation could split a
// UTF-8 sequence and produce an invalid embed.
func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	return string(r[:max-1]) + "…"
}

// currentVersion is the kuso server version stamped at build time. We
// read it lazily via a getter rather than a fixed init so tests can
// override it without touching the version package. The default empty
// string just means "no version suffix on the footer."
var currentVersion = ""

// SetVersion installs the server version string the renderer surfaces
// in the embed footer. Called once from cmd/kuso-server/main during
// boot. Optional — empty version just omits the suffix.
func SetVersion(v string) {
	currentVersion = strings.TrimSpace(v)
}

// sendWebhook POSTs the raw event JSON to a generic URL. When secret
// is set we sign the body and include the signature in three headers
// receivers expect:
//
//	X-Hub-Signature-256: sha256=<hex>   — GitHub-shaped, the most
//	                                       widely-supported format.
//	X-Kuso-Signature:    <hex>           — kuso-native, easier to
//	                                       parse for hand-rolled
//	                                       consumers.
//	X-Kuso-Timestamp:    <unix-seconds>  — replay-window enforcement
//	                                       for receivers that care.
//
// Pre-v0.9.4 the secret was read from the DB and immediately
// `_ = secret`'d — receivers configured a secret expecting
// X-Hub-Signature-256 and got nothing. This was a real functional
// gap, flagged in the v0.9.3 audit.
func (d *Dispatcher) sendWebhook(ctx context.Context, url, secret string, e Event) {
	d.post(ctx, url, e, signatureHeaders(secret, e))
}

// signatureHeaders computes the X-Hub-Signature-256 / X-Kuso-* HMAC
// headers for a webhook. Returns an empty Header when secret is
// empty (skips signing entirely — receivers without a configured
// secret don't expect a signature).
//
// The body is the same JSON marshal postSync produces. Marshaling
// twice (here + in postSync) is the cost; keeps this function
// stateless.
func signatureHeaders(secret string, body any) http.Header {
	out := http.Header{}
	if secret == "" {
		return out
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return out
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := hex.EncodeToString(mac.Sum(nil))
	out.Set("X-Hub-Signature-256", "sha256="+sig)
	out.Set("X-Kuso-Signature", sig)
	out.Set("X-Kuso-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	return out
}

func (d *Dispatcher) post(ctx context.Context, url string, body any, extra http.Header) {
	if err := d.postSync(ctx, url, body, extra); err != nil {
		// Swallow + log: the async fire-and-forget path doesn't
		// have a caller to surface the error to.
		d.logger.Warn("notify: post", "url", redact(url), "err", err)
	}
}

// postSync is post with the error returned to the caller. Used by
// SendDirect so the Test endpoint can show "401 from Discord" or
// similar in the UI instead of a misleading 204.
func (d *Dispatcher) postSync(ctx context.Context, url string, body any, extra http.Header) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "kuso-server")
	for k, vs := range extra {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		// Read up to 256 bytes of the response body so the user
		// sees the actual upstream error (Discord returns useful
		// JSON: {"message":"Invalid Webhook Token","code":50027}).
		buf, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(buf)))
	}
	return nil
}

// sendDiscordSync mirrors sendDiscord but returns the upstream error.
func (d *Dispatcher) sendDiscordSync(ctx context.Context, url string, e Event, mention string) error {
	return d.postSync(ctx, url, discordPayload(e, mention), nil)
}

// sendWebhookSync mirrors sendWebhook with error propagation.
func (d *Dispatcher) sendWebhookSync(ctx context.Context, url, secret string, e Event) error {
	return d.postSync(ctx, url, e, signatureHeaders(secret, e))
}

// redact strips secret tokens from a webhook URL (or any string that
// embeds one) before it's logged or stored. Two shapes are handled:
//
//   - Telegram: `https://api.telegram.org/bot<TOKEN>/sendMessage`. The
//     token sits mid-URL between `/bot` and the next `/`, so the
//     trailing-segment rule below wouldn't catch it. Replaced first,
//     in place, so an error string like
//     `Post "https://api.telegram.org/bot123:ABC/sendMessage": …` is
//     scrubbed even when it embeds the URL rather than being one.
//   - Discord et al.: the secret is the last `/`-segment
//     (`/.../<id>/<token>`); drop it, keeping the id so different
//     channels stay distinguishable in logs.
func redact(url string) string {
	if strings.Contains(url, "api.telegram.org") {
		// Telegram token is mid-URL, not the trailing segment; redactTelegram
		// handles it. Applying the trailing-segment rule below would then
		// wrongly chop the /sendMessage method off the end.
		return redactTelegram(url)
	}
	// Only apply the trailing-segment rule to a bare URL, not to a
	// larger error string — otherwise we'd chop the tail of an arbitrary
	// message. A bare URL has no spaces.
	if strings.ContainsAny(url, " \t") {
		return url
	}
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			return url[:i+1] + "..."
		}
	}
	return url
}

// redactTelegram replaces the bot token in any Telegram Bot API URL
// with a placeholder, wherever that URL appears in s (including inside
// a wrapped HTTP-client error string like `Post "https://…/bot<tok>/…"`).
// Matches `api.telegram.org/bot<token>` and rewrites the token to `...`.
// Safe on strings with no Telegram URL.
func redactTelegram(s string) string {
	const marker = "api.telegram.org/bot"
	var b strings.Builder
	for {
		i := strings.Index(s, marker)
		if i < 0 {
			b.WriteString(s)
			return b.String()
		}
		tokStart := i + len(marker)
		b.WriteString(s[:tokStart])
		rest := s[tokStart:]
		// Token runs until the next path separator or the end of the URL
		// token (a quote/space in a wrapped error string). Everything up
		// to that delimiter is the secret.
		end := strings.IndexAny(rest, "/\" \t")
		if end < 0 {
			end = len(rest)
		}
		b.WriteString("...")
		s = rest[end:]
	}
}

// Format helpers used by callers to keep event creation tidy.

// inAppURL returns the dashboard path the bell-icon notification
// click should land on for this kind of event. Web-side, the popover
// renders <a href={event.url}> when populated, falling back to a
// non-interactive row when blank. Paths use simple anchors that the
// SPA Router resolves; no query strings so they survive the static
// export.
func serviceURL(project, service string) string {
	if project == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("/projects/%s?service=%s", project, service)
}

// absoluteURL upgrades an in-app path like "/projects/foo?service=bar"
// to a full https URL for external sinks (Discord, Slack) that reject
// relative paths in embed `url` fields. Already-absolute URLs pass
// through unchanged. Returns "" when no base URL can be derived — the
// caller MUST omit the `url` field in that case rather than send an
// invalid embed (Discord 400s the entire payload otherwise).
//
// Preference order matches handlers/node_bootstrap.go's publicBaseURL,
// minus the request-scoped XFF logic (we have no *http.Request here):
//  1. KUSO_PUBLIC_URL — operator-set source of truth.
//  2. https://$KUSO_DOMAIN — install.sh sets this on every deployment.
func absoluteURL(in string) string {
	if in == "" {
		return ""
	}
	if strings.HasPrefix(in, "http://") || strings.HasPrefix(in, "https://") {
		return in
	}
	if !strings.HasPrefix(in, "/") {
		return ""
	}
	base := publicBase()
	if base == "" {
		return ""
	}
	return base + in
}

func projectURL(project string) string {
	if project == "" {
		return ""
	}
	return "/projects/" + project
}

func BuildSucceeded(project, service, ref, deployURL string) Event {
	// Builds land on the service overlay's Deployments tab so the
	// user can see the new revision in context. deployURL came in
	// from the build pipeline and was sometimes the deployed app's
	// public URL (different intent), so we use the in-app link.
	return Event{
		Type:     EventBuildSucceeded,
		Title:    fmt.Sprintf("✓ Build succeeded: %s", service),
		Body:     fmt.Sprintf("ref `%s`", shortRef(ref)),
		Project:  project,
		Service:  service,
		URL:      serviceURL(project, service),
		Severity: "info",
		Extra:    map[string]string{"deployURL": deployURL, "ref": shortRef(ref)},
	}
}

func BuildFailed(project, service, ref, reason string) Event {
	// Failed builds → service Deployments tab so the user sees the
	// failed entry + can hit "view logs" / "redeploy".
	return Event{
		Type:     EventBuildFailed,
		Title:    fmt.Sprintf("✗ Build failed: %s", service),
		Body:     reason,
		Project:  project,
		Service:  service,
		URL:      serviceURL(project, service),
		Severity: "error",
		Extra:    map[string]string{"ref": shortRef(ref)},
	}
}

// runEventURL deep-links into the Runs tab of the service overlay.
// Mirrors serviceURL but pins ?tab=runs so a click from Discord
// lands on the right surface.
func runEventURL(project, service string) string {
	if project == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("/projects/%s?service=%s&tab=runs", project, service)
}

// formatShortDuration renders a duration compactly: "5m", "1h 24m",
// "2d 3h". Mirrors the build-card duration style; lives in notify
// because the node helpers use it too.
func formatShortDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d/time.Minute))
	}
	if d < 24*time.Hour {
		h := int(d / time.Hour)
		m := int((d % time.Hour) / time.Minute)
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh %dm", h, m)
	}
	days := int(d / (24 * time.Hour))
	h := int((d % (24 * time.Hour)) / time.Hour)
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, h)
}

// AlertURL is the deep-link for an alert.fired event: the service
// overlay when the rule targets a service, the project canvas when it
// targets a project, else the alerts settings page for rule context.
func AlertURL(project, service string) string {
	if project != "" && service != "" {
		return serviceURL(project, service)
	}
	if project != "" {
		return projectURL(project)
	}
	return "/settings/alerts"
}

func shortRef(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
