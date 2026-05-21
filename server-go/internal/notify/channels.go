package notify

// channels.go holds the non-Discord/non-webhook delivery channels:
// Slack, Mattermost, Telegram, Pushover (all JSON-POST APIs) and
// Email (SMTP). Each exposes a `<kind>Sync` sender returning the
// upstream error so the Test endpoint and the outbox retry loop both
// surface real failures.
//
// Slack and Mattermost both accept Slack's "incoming webhook" JSON
// shape — a `{text, attachments[]}` body — so they share one renderer.
// Telegram and Pushover have their own small JSON shapes. Email is a
// plain-text SMTP message.

import (
	"context"
	"fmt"
	"mime"
	"net/smtp"
	"net/url"
	"strings"
	"time"
)

// plainSummary renders an Event as a compact plain-text block used by
// the text-oriented channels (Telegram, Pushover, Email). Format:
//
//	<emoji> <Title>
//	<description / body>
//	<Field: Value> lines
//	<project · service>   <time>
//	<url>
func plainSummary(e Event) string {
	var b strings.Builder
	b.WriteString(severityEmoji(e) + " " + e.Title + "\n")
	desc := strings.TrimSpace(e.Description)
	if desc == "" {
		desc = strings.TrimSpace(e.Body)
	}
	if desc != "" {
		b.WriteString(desc + "\n")
	}
	for _, f := range e.Fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		b.WriteString(f.Name + ": " + f.Value + "\n")
	}
	scope := e.Project
	if e.Service != "" {
		scope += " · " + e.Service
	}
	when := e.Timestamp.UTC().Format(time.RFC1123)
	if scope != "" {
		b.WriteString("\n" + scope + "   " + when + "\n")
	} else {
		b.WriteString("\n" + when + "\n")
	}
	if abs := absoluteURL(e.URL); abs != "" {
		b.WriteString(abs + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// severityEmoji maps an event's severity/type to a leading glyph so
// the text channels carry the same at-a-glance signal the Discord
// embed colour does.
func severityEmoji(e Event) string {
	if e.Severity == "error" {
		return "🔴"
	}
	if e.Severity == "warn" {
		return "🟠"
	}
	switch e.Type {
	case EventBuildSucceeded, EventDeployRolled, EventBackupOK:
		return "🟢"
	case EventBuildFailed, EventPodCrashed, EventAlertFired, EventBackupFailed:
		return "🔴"
	default:
		return "🔵"
	}
}

// --- Slack / Mattermost ---------------------------------------------

// slackPayload builds the Slack "incoming webhook" body. Mattermost
// accepts the same shape, so both channels reuse this. The colour bar
// comes from the same severity logic as the Discord embed.
func slackPayload(e Event) map[string]any {
	desc := strings.TrimSpace(e.Description)
	if desc == "" {
		desc = strings.TrimSpace(e.Body)
	}
	att := map[string]any{
		"fallback": e.Title,
		"color":    slackColor(e),
		"title":    e.Title,
		"text":     desc,
		"ts":       e.Timestamp.UTC().Unix(),
	}
	if abs := absoluteURL(e.URL); abs != "" {
		att["title_link"] = abs
	}
	fields := make([]map[string]any, 0, len(e.Fields))
	for _, f := range e.Fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		fields = append(fields, map[string]any{
			"title": truncateRunes(f.Name, 256),
			"value": truncateRunes(f.Value, 1024),
			"short": f.Inline,
		})
	}
	if len(fields) > 0 {
		att["fields"] = fields
	}
	scope := e.Project
	if e.Service != "" {
		scope += " · " + e.Service
	}
	if scope != "" {
		att["footer"] = scope
	}
	if tail := strings.TrimSpace(e.LogTail); tail != "" {
		// Slack renders triple-backtick as a monospace block.
		att["text"] = strings.TrimSpace(desc + "\n```\n" + truncateRunes(tail, 2000) + "\n```")
	}
	return map[string]any{
		"text":        severityEmoji(e) + " " + e.Title,
		"attachments": []map[string]any{att},
	}
}

// slackColor returns a hex colour string for the Slack attachment bar.
func slackColor(e Event) string {
	if e.Severity == "error" {
		return "#EF4444"
	}
	if e.Severity == "warn" {
		return "#F59E0B"
	}
	switch e.Type {
	case EventBuildSucceeded, EventDeployRolled, EventBackupOK:
		return "#10B981"
	case EventBuildFailed, EventPodCrashed, EventAlertFired, EventBackupFailed:
		return "#EF4444"
	default:
		return "#40476D"
	}
}

// sendSlackSync posts the Slack/Mattermost incoming-webhook payload.
func (d *Dispatcher) sendSlackSync(ctx context.Context, url string, e Event) error {
	return d.postSync(ctx, url, slackPayload(e), nil)
}

// --- Telegram -------------------------------------------------------

// sendTelegramSync posts to the Telegram Bot API sendMessage method.
// Config carries {botToken, chatId}. The message is plain text (no
// parse_mode) so a stray markdown/HTML char in a log tail can't break
// rendering or get rejected.
func (d *Dispatcher) sendTelegramSync(ctx context.Context, botToken, chatID string, e Event) error {
	if botToken == "" || chatID == "" {
		return fmt.Errorf("telegram channel needs botToken and chatId")
	}
	api := "https://api.telegram.org/bot" + url.PathEscape(botToken) + "/sendMessage"
	body := map[string]any{
		"chat_id":                  chatID,
		"text":                     truncateRunes(plainSummary(e), 4096),
		"disable_web_page_preview": true,
	}
	return d.postSync(ctx, api, body, nil)
}

// --- Pushover -------------------------------------------------------

// sendPushoverSync posts to the Pushover messages API. Config carries
// {token (application API token), user (user/group key)}. Pushover
// priority is derived from severity: error → 1 (high), else 0.
func (d *Dispatcher) sendPushoverSync(ctx context.Context, token, user string, e Event) error {
	if token == "" || user == "" {
		return fmt.Errorf("pushover channel needs token and user")
	}
	priority := 0
	if e.Severity == "error" {
		priority = 1
	}
	body := map[string]any{
		"token":    token,
		"user":     user,
		"title":    truncateRunes(e.Title, 250),
		"message":  truncateRunes(plainSummary(e), 1024),
		"priority": priority,
	}
	if abs := absoluteURL(e.URL); abs != "" {
		body["url"] = abs
		body["url_title"] = "Open in kuso"
	}
	return d.postSync(ctx, "https://api.pushover.net/1/messages.json", body, nil)
}

// --- Email (SMTP) ---------------------------------------------------

// sendEmailSync delivers an Event as a plain-text email over SMTP.
// Config carries {host, port, username, password, from, to}. STARTTLS
// is used when the server offers it; plain auth otherwise. `to` may be
// a comma-separated list.
func (d *Dispatcher) sendEmailSync(ctx context.Context, cfg map[string]any, e Event) error {
	host, _ := cfg["host"].(string)
	from, _ := cfg["from"].(string)
	toRaw, _ := cfg["to"].(string)
	username, _ := cfg["username"].(string)
	password, _ := cfg["password"].(string)
	if host == "" || from == "" || toRaw == "" {
		return fmt.Errorf("email channel needs host, from and to")
	}
	port := "587"
	if p, ok := cfg["port"].(string); ok && p != "" {
		port = p
	}
	recipients := make([]string, 0, 4)
	for _, r := range strings.Split(toRaw, ",") {
		if r = strings.TrimSpace(r); r != "" {
			recipients = append(recipients, r)
		}
	}
	if len(recipients) == 0 {
		return fmt.Errorf("email channel has no valid recipients")
	}

	subject := severityEmoji(e) + " " + e.Title
	msg := "From: " + from + "\r\n" +
		"To: " + strings.Join(recipients, ", ") + "\r\n" +
		"Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"\r\n" +
		plainSummary(e) + "\r\n"

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	// smtp.SendMail does not take a context; run it in a goroutine and
	// select on ctx so a hung SMTP server can't pin the outbox worker.
	done := make(chan error, 1)
	go func() {
		done <- smtp.SendMail(host+":"+port, auth, from, recipients, []byte(msg))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-done:
		if err != nil {
			return fmt.Errorf("smtp: %w", err)
		}
		return nil
	}
}
