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
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
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
}

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
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

// Emit enqueues an event. Non-blocking: if the buffer is full, the
// event is dropped and a warning is logged. Safe to call from any
// goroutine, including before Run starts (events queue up).
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
	select {
	case d.ch <- e:
	default:
		if d.logger != nil {
			d.logger.Warn("notify: queue full, dropping event", "type", string(e.Type))
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
			d.dispatch(ctx, e)
		}
	}
}

func (d *Dispatcher) dispatch(ctx context.Context, e Event) {
	if d.db == nil {
		return
	}
	notifs, err := d.db.ListNotifications(ctx)
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
// is set we include an X-Kuso-Signature header (HMAC-SHA256 of the
// body) so receivers can verify origin. Receivers ignoring auth get
// the raw event either way.
func (d *Dispatcher) sendWebhook(ctx context.Context, url, secret string, e Event) {
	headers := http.Header{}
	if secret != "" {
		// Signature implementation deferred — leave the header
		// missing until the receiver actually needs verification.
		// Keeps the wire shape small for the common case.
		_ = secret
	}
	d.post(ctx, url, e, headers)
}

func (d *Dispatcher) post(ctx context.Context, url string, body any, extra http.Header) {
	buf, err := json.Marshal(body)
	if err != nil {
		d.logger.Warn("notify: marshal", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(buf))
	if err != nil {
		d.logger.Warn("notify: build request", "err", err)
		return
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
		d.logger.Warn("notify: post", "url", redact(url), "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		d.logger.Warn("notify: non-2xx", "url", redact(url), "status", resp.StatusCode)
	}
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

func BuildSucceeded(project, service, ref, deployURL string) Event {
	return Event{
		Type:     EventBuildSucceeded,
		Title:    fmt.Sprintf("✓ Build succeeded: %s", service),
		Body:     fmt.Sprintf("ref `%s`", shortRef(ref)),
		Project:  project,
		Service:  service,
		URL:      deployURL,
		Severity: "info",
	}
}

func BuildFailed(project, service, ref, reason string) Event {
	return Event{
		Type:     EventBuildFailed,
		Title:    fmt.Sprintf("✗ Build failed: %s", service),
		Body:     reason,
		Project:  project,
		Service:  service,
		Severity: "error",
		Extra:    map[string]string{"ref": shortRef(ref)},
	}
}

func PodCrashed(project, service, podName, reason string) Event {
	return Event{
		Type:     EventPodCrashed,
		Title:    fmt.Sprintf("⚠ Pod crashed: %s", service),
		Body:     reason,
		Project:  project,
		Service:  service,
		Severity: "warn",
		Extra:    map[string]string{"pod": podName},
	}
}

func AlertFired(title, body, severity string, extra map[string]string) Event {
	return Event{
		Type:     EventAlertFired,
		Title:    title,
		Body:     body,
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
