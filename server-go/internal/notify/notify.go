// Package notify is the event fan-out for kuso. Domain code emits
// events ("build.succeeded", "pod.crashed", etc.); the dispatcher
// reads notification configs from the DB and pushes formatted
// payloads to every enabled sink (Discord webhook, generic webhook,
// Slack later).
//
// Design constraints:
//   - Non-blocking: domain code never waits on a slow webhook. Events
//     enqueue onto a buffered channel; the dispatcher drains in its
//     own goroutine.
//   - Per-event filtering: the DB row carries an `events` whitelist;
//     empty list = all events. Rows can be disabled without deletion.
//   - Per-pipeline (project) filtering: future-friendly — today we
//     send everything globally, but the column is there.
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
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"kuso/server/internal/db"
)

// EventType is one of a fixed set so consumers can filter cleanly.
type EventType string

const (
	EventBuildStarted   EventType = "build.started"
	EventBuildSucceeded EventType = "build.succeeded"
	EventBuildFailed    EventType = "build.failed"
	EventDeployRolled   EventType = "deploy.rolled"
	EventPodCrashed     EventType = "pod.crashed"
	EventAlertFired     EventType = "alert.fired"
	EventBackupOK       EventType = "backup.succeeded"
	EventBackupFailed   EventType = "backup.failed"
	// Node lifecycle events. Fired by the nodewatch goroutine when a
	// kube node has been NotReady past the watcher's threshold.
	// Recovery emits EventNodeRecovered so the operator sees both
	// edges of the outage.
	EventNodeUnreachable EventType = "node.unreachable"
	EventNodeRecovered   EventType = "node.recovered"
)

// Event is the wire-stable payload domain code emits. JSON-serialised
// straight to webhook sinks; rendered to embeds for Discord/Slack.
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
}

// Dispatcher is the fan-out service. Construct via New + start with
// Run in a goroutine.
type Dispatcher struct {
	db     *db.DB
	logger *slog.Logger
	ch     chan Event
	client *http.Client

	mu          sync.Mutex
	closed      bool
	dropOnFloor bool

	// notifsCache is a short-lived cache of the configured notification
	// channels. Without it, every event drained from `ch` does a fresh
	// SQLite SELECT + JSON decode — which on a build storm + a single-
	// connection writer pool starves every other writer (audit log,
	// nodemetrics insert, login). Cache lives for notifsCacheTTL and
	// is invalidated explicitly when the notifications handler does a
	// CREATE/UPDATE/DELETE so admins see their config changes apply
	// to the next event without waiting for the TTL.
	notifsMu      sync.RWMutex
	notifsCache   []db.Notification
	notifsExpires time.Time
}

// notifsCacheTTL bounds how stale the dispatcher's view of the
// notifications table can be. 30s matches the bell-icon polling
// cadence and is short enough that a misconfigured channel can be
// disabled without restarting the server.
const notifsCacheTTL = 30 * time.Second

// New returns a dispatcher bound to a DB for config lookup. queueSize
// caps the in-memory event buffer; events past that point are dropped
// (we'd rather lose a notification than wedge the build poller).
func New(database *db.DB, logger *slog.Logger, queueSize int) *Dispatcher {
	if queueSize <= 0 {
		queueSize = 256
	}
	return &Dispatcher{
		db:     database,
		logger: logger,
		ch:     make(chan Event, queueSize),
		client: &http.Client{
			Timeout: 8 * time.Second,
			// SSRF-safe transport: rejects connections to link-local,
			// loopback, and private (RFC1918 + RFC4193) ranges. A
			// user with notification:write could otherwise point a
			// webhook at 169.254.169.254 (cloud metadata service)
			// or 10.0.0.0/8 (in-cluster apiserver / addon DBs) and
			// exfiltrate data through the redirect/error response.
			Transport: ssrfSafeTransport(),
		},
	}
}

// ssrfSafeTransport returns a Transport whose dialer refuses to
// connect to addresses in private/reserved ranges. Inspired by
// google/safehttp's safedialer; we keep it tiny so we don't pull
// the dep.
func ssrfSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if isReservedIP(ip) {
					return nil, fmt.Errorf("notify: refusing to dial reserved address %s (%s)", ip, host)
				}
			}
			// Re-dial against the resolved IP so we don't race a
			// rebinding DNS attack between our check and the dial.
			if len(ips) == 0 {
				return nil, fmt.Errorf("notify: no IPs for %s", host)
			}
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
		},
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 8 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// isReservedIP returns true for addresses we don't want webhook
// targets to reach: loopback, link-local, private RFC1918, ULA
// (RFC4193), unspecified, multicast. Public-cloud metadata services
// live in 169.254.169.254 (link-local), GCE/AWS at the same address;
// blocking link-local covers both.
//
// Operators with an internal-only install (no internet egress, all
// notification sinks are in-cluster) can opt out by setting
// KUSO_NOTIFY_ALLOW_PRIVATE_IPS=true. This is a foot-gun knob —
// document it as such.
func isReservedIP(ip net.IP) bool {
	if isAllowPrivateIPs() {
		// Still block obvious local-only addresses that have no
		// reasonable webhook use.
		return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
			ip.IsInterfaceLocalMulticast() || ip.IsUnspecified()
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	// IsPrivate covers 10/8, 172.16/12, 192.168/16, fc00::/7, fec0::/10.
	if ip.IsPrivate() {
		return true
	}
	return false
}

// isAllowPrivateIPs reads the env var on each call so a config
// change doesn't require a server restart. Cheap (one os.Getenv).
func isAllowPrivateIPs() bool {
	return os.Getenv("KUSO_NOTIFY_ALLOW_PRIVATE_IPS") == "true"
}

// Emit enqueues an event AND persists it to the in-app feed
// synchronously. The persist step makes the bell-icon feed durable
// even when the in-memory dispatch channel overflows — at most we
// lose webhook fan-out on a burst, never the user-visible feed.
//
// The dispatch channel is bounded (256 by default) and Emit is non-
// blocking. On overflow we log a warn but the event is already in
// SQLite. A future v0.9 will move dispatch to a DB-backed work queue
// so even webhook fan-out is durable across restarts; this is the
// half-step that closes the silent-drop bug today.
func (d *Dispatcher) Emit(e Event) {
	if d == nil {
		return
	}
	if e.Timestamp.IsZero() {
		e.Timestamp = time.Now().UTC()
	}
	d.mu.Lock()
	closed := d.closed
	d.mu.Unlock()
	if closed {
		return
	}
	// Persist first — synchronous to the caller. SQLite's
	// busy_timeout will retry under contention; on hard failure we
	// log and proceed (the channel send is still attempted, so a
	// transient DB blip doesn't lose the webhook fan-out).
	if d.db != nil {
		// Wrap the timeout-context dance in a closure so cancel() is
		// deferred — a panic between WithTimeout and the explicit
		// cancel() (e.g. from the DB driver) would otherwise leak the
		// context goroutine until the parent (Background) is collected,
		// i.e. forever.
		func() {
			persistCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := d.db.InsertNotificationEvent(persistCtx, db.NotificationEvent{
				Type:     string(e.Type),
				Title:    e.Title,
				Body:     e.Body,
				Severity: e.Severity,
				Project:  e.Project,
				Service:  e.Service,
				URL:      e.URL,
				Extra:    e.Extra,
			}); err != nil && d.logger != nil {
				d.logger.Warn("notify: persist event", "err", err, "type", string(e.Type))
			}
		}()
	}
	select {
	case d.ch <- e:
	default:
		// Channel full → webhook fan-out drops this event, but the
		// bell-icon feed has it from the persist above. Operators
		// who care about webhook reliability should bump
		// KUSO_NOTIFY_QUEUE_SIZE.
		if d.logger != nil {
			d.logger.Warn("notify: dispatch queue full, webhook fanout skipped",
				"type", string(e.Type), "queue_cap", cap(d.ch))
		}
	}
}

// Run consumes the event channel and dispatches to every enabled
// notification sink. Exits when ctx is canceled. Call once in a
// background goroutine.
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			d.mu.Lock()
			d.closed = true
			d.mu.Unlock()
			return
		case e := <-d.ch:
			// Persist now happens in Emit (synchronously w.r.t. the
			// caller) so the in-app feed survives queue overflow.
			// Run's job is purely to fan out to webhook sinks.
			d.dispatch(ctx, e)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, e Event) {
	if d.db == nil {
		return
	}
	notifs, err := d.cachedNotifications(ctx)
	if err != nil {
		d.logger.Warn("notify: list configs", "err", err)
		return
	}
	for _, n := range notifs {
		if !n.Enabled {
			continue
		}
		if !eventMatches(string(e.Type), n.Events) {
			continue
		}
		switch n.Type {
		case "discord":
			url, _ := n.Config["url"].(string)
			if url == "" {
				continue
			}
			d.sendDiscord(ctx, url, e, mentionFor(e, n.Config))
		case "webhook":
			url, _ := n.Config["url"].(string)
			if url == "" {
				continue
			}
			secret, _ := n.Config["secret"].(string)
			d.sendWebhook(ctx, url, secret, e)
		}
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
	switch n.Type {
	case "discord":
		url, _ := n.Config["url"].(string)
		if url == "" {
			return fmt.Errorf("channel %q has no webhook URL", n.Name)
		}
		return d.sendDiscordSync(ctx, url, e, mentionFor(e, n.Config))
	case "webhook":
		url, _ := n.Config["url"].(string)
		if url == "" {
			return fmt.Errorf("channel %q has no webhook URL", n.Name)
		}
		secret, _ := n.Config["secret"].(string)
		return d.sendWebhookSync(ctx, url, secret, e)
	default:
		return fmt.Errorf("unsupported notification type %q", n.Type)
	}
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
	color := discordColor(e)
	embed := map[string]any{
		"title":       e.Title,
		"description": e.Body,
		"color":       color,
		"timestamp":   e.Timestamp.Format(time.RFC3339),
		"fields":      discordFields(e),
	}
	if e.URL != "" {
		embed["url"] = e.URL
	}
	body := map[string]any{
		"username": "kuso",
		"embeds":   []any{embed},
	}
	if mention != "" {
		body["content"] = mention
		// Allowed_mentions explicitly enables the parsing — without
		// this Discord strips @here / @everyone for hardened webhooks.
		// Roles need explicit IDs in `roles`.
		body["allowed_mentions"] = allowedMentionsFor(mention)
	}
	d.post(ctx, url, body, nil)
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

func discordColor(e Event) int {
	if e.Severity == "error" {
		return 0xEB6534 // accent orange
	}
	if e.Severity == "warn" {
		return 0xF59E0B
	}
	switch e.Type {
	case EventBuildSucceeded, EventDeployRolled, EventBackupOK:
		return 0x10B981 // emerald
	case EventBuildFailed, EventPodCrashed, EventAlertFired, EventBackupFailed:
		return 0xEF4444 // red
	default:
		return 0x40476D // navy (matches the logo)
	}
}

func discordFields(e Event) []map[string]any {
	out := make([]map[string]any, 0, 4)
	if e.Project != "" {
		out = append(out, map[string]any{"name": "Project", "value": e.Project, "inline": true})
	}
	if e.Service != "" {
		out = append(out, map[string]any{"name": "Service", "value": e.Service, "inline": true})
	}
	for k, v := range e.Extra {
		out = append(out, map[string]any{"name": k, "value": v, "inline": true})
	}
	return out
}

// sendWebhook POSTs the raw event JSON to a generic URL. When secret
// is set we sign the body and include the signature in three headers
// receivers expect:
//
//   X-Hub-Signature-256: sha256=<hex>   — GitHub-shaped, the most
//                                          widely-supported format.
//   X-Kuso-Signature:    <hex>           — kuso-native, easier to
//                                          parse for hand-rolled
//                                          consumers.
//   X-Kuso-Timestamp:    <unix-seconds>  — replay-window enforcement
//                                          for receivers that care.
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
	color := discordColor(e)
	embed := map[string]any{
		"title":       e.Title,
		"description": e.Body,
		"color":       color,
		"timestamp":   e.Timestamp.Format(time.RFC3339),
		"fields":      discordFields(e),
	}
	if e.URL != "" {
		embed["url"] = e.URL
	}
	body := map[string]any{"username": "kuso", "embeds": []any{embed}}
	if mention != "" {
		body["content"] = mention
		body["allowed_mentions"] = allowedMentionsFor(mention)
	}
	return d.postSync(ctx, url, body, nil)
}

// sendWebhookSync mirrors sendWebhook with error propagation.
func (d *Dispatcher) sendWebhookSync(ctx context.Context, url, secret string, e Event) error {
	return d.postSync(ctx, url, e, signatureHeaders(secret, e))
}

// redact strips the secret token from a Discord webhook URL when
// logging — Discord URLs end in `/.../<id>/<token>`. We keep the
// id segment so different channels stay distinguishable in logs.
func redact(url string) string {
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			return url[:i+1] + "..."
		}
	}
	return url
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

func PodCrashed(project, service, podName, reason string) Event {
	// Crashes → service overlay (Logs tab in particular is what the
	// user wants next, but the overlay router defaults to Logs when
	// the service has crashed pods, so a single deep-link works).
	return Event{
		Type:     EventPodCrashed,
		Title:    fmt.Sprintf("⚠ Pod crashed: %s", service),
		Body:     reason,
		Project:  project,
		Service:  service,
		URL:      serviceURL(project, service),
		Severity: "warn",
		Extra:    map[string]string{"pod": podName},
	}
}

// NodeUnreachable fires when a node has been NotReady past the
// nodewatch threshold (5 min by default). The watcher cordons the
// node before emitting so the event narrates a state change the
// operator can act on, not a transient blip.
func NodeUnreachable(node, reason string) Event {
	return Event{
		Type:     EventNodeUnreachable,
		Title:    fmt.Sprintf("✗ Node unreachable: %s", node),
		Body:     reason,
		URL:      "/settings/nodes",
		Severity: "error",
		Extra:    map[string]string{"node": node},
	}
}

// NodeRecovered fires when a previously-cordoned-as-unreachable node
// transitions back to Ready. The watcher uncordons it (so workloads
// can land again) before emitting.
func NodeRecovered(node string) Event {
	return Event{
		Type:     EventNodeRecovered,
		Title:    fmt.Sprintf("✓ Node recovered: %s", node),
		Body:     "node is Ready again and uncordoned",
		URL:      "/settings/nodes",
		Severity: "info",
		Extra:    map[string]string{"node": node},
	}
}

func AlertFired(title, body, severity string, extra map[string]string) Event {
	// Alert events know where to deep-link via Extra: when the rule
	// targeted a service we can land there; otherwise fall through
	// to the alerts page so the user sees rule context.
	url := "/settings/alerts"
	if extra != nil {
		if p, s := extra["project"], extra["service"]; p != "" && s != "" {
			url = serviceURL(p, s)
		} else if p := extra["project"]; p != "" {
			url = projectURL(p)
		}
	}
	return Event{
		Type:     EventAlertFired,
		Title:    title,
		Body:     body,
		URL:      url,
		Severity: severity,
		Extra:    extra,
	}
}

func shortRef(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
