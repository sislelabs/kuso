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
	"crypto/tls"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"kuso/server/internal/httpx"
)

// plainLogLines is how many trailing LogTail lines the text channels
// carry — enough to show the failing line, short enough for Pushover.
const plainLogLines = 8

// plainSummary renders an Event as a compact plain-text block used by
// the text-oriented channels (Telegram, Pushover, Email). Format:
//
//	<emoji> <Title>
//	<description / body>
//	<Field: Value> lines
//	<last few log lines>
//	<project · service>   <time>
//	<url>
//
// limit > 0 caps the result in runes. The log tail absorbs the cut
// (keeping its LAST lines) so the scope + URL footer survives; only if
// that isn't enough is the whole text truncated.
func plainSummary(e Event, limit int) string {
	const m = markupPlain
	var head strings.Builder
	head.WriteString(severityEmoji(e) + " " + e.Title + "\n")
	desc := cardDescription(e)
	if desc != "" {
		head.WriteString(discordToMarkup(expandTimes(desc, m), m) + "\n")
	}
	if diag := diagnosisText(e.Classification, m); diag != "" {
		head.WriteString(diag + "\n")
	}
	for _, f := range e.Fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		head.WriteString(f.Name + ": " + discordToMarkup(expandTimes(f.Value, m), m) + "\n")
	}

	var foot strings.Builder
	scope := e.Project
	if e.Service != "" {
		scope += " · " + e.Service
	}
	when := e.Timestamp.UTC().Format(time.RFC1123)
	if scope != "" {
		foot.WriteString("\n" + scope + "   " + when + "\n")
	} else {
		foot.WriteString("\n" + when + "\n")
	}
	if links := linksLine(e, m); links != "" {
		foot.WriteString(links + "\n")
	} else if abs := absoluteURL(e.URL); abs != "" {
		foot.WriteString(abs + "\n")
	}

	logs := ""
	if tail := strings.TrimSpace(e.LogTail); tail != "" {
		lines := strings.Split(tail, "\n")
		if len(lines) > plainLogLines {
			lines = lines[len(lines)-plainLogLines:]
		}
		logs = "\n" + strings.Join(lines, "\n") + "\n"
		if limit > 0 {
			room := limit - len([]rune(head.String())) - len([]rune(foot.String()))
			logs = keepTailRunes(logs, room)
		}
	}
	out := strings.TrimRight(head.String()+logs+foot.String(), "\n")
	if limit > 0 {
		out = truncateRunes(out, limit)
	}
	return out
}

// keepTailRunes keeps the last max runes of s, prefixing "…" when it
// cut, or returns "" when there's no useful room.
func keepTailRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max < 16 {
		return ""
	}
	return "\n…" + string(r[len(r)-(max-2):])
}

// severityEmoji is the text-channel counterpart of discordColor.
func severityEmoji(e Event) string {
	switch eventTone(e) {
	case toneError:
		return "🔴"
	case toneWarn:
		return "🟠"
	case toneSuccess:
		return "🟢"
	case toneNeutral:
		return "⚪"
	default:
		return "🔵"
	}
}

// --- Slack / Mattermost ---------------------------------------------

// slackPayload builds the Slack "incoming webhook" body. Mattermost
// accepts the same shape, so both channels reuse this. The colour bar
// comes from the same severity logic as the Discord embed.
func slackPayload(e Event) map[string]any {
	const m = markupSlack
	desc := cardDescription(e)
	desc = discordToMarkup(expandTimes(desc, m), m)
	if diag := diagnosisText(e.Classification, m); diag != "" {
		desc = strings.TrimSpace(desc + "\n" + diag)
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
			"value": truncateRunes(discordToMarkup(expandTimes(f.Value, m), m), 1024),
			"short": f.Inline,
		})
	}
	if len(fields) > 0 {
		att["fields"] = fields
	}
	footer := e.Footer
	if footer == "" {
		footer = instanceHost()
	}
	if footer != "" {
		att["footer"] = footer
	}
	text := desc
	if tail := strings.TrimSpace(e.LogTail); tail != "" && !isCompact(e) {
		// Slack renders triple-backtick as a monospace block.
		text = strings.TrimSpace(text + "\n```\n" + truncateRunes(tail, 2000) + "\n```")
	}
	if links := linksLine(e, m); links != "" {
		text = strings.TrimSpace(text + "\n" + links)
	}
	att["text"] = text
	return map[string]any{
		"text":        severityEmoji(e) + " " + e.Title,
		"attachments": []map[string]any{att},
	}
}

// slackColor returns a hex colour string for the Slack attachment bar,
// matching discordColor.
func slackColor(e Event) string {
	return fmt.Sprintf("#%06X", discordColor(e))
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
		"text":                     plainSummary(e, 4096),
		"disable_web_page_preview": true,
	}
	// The bot token is embedded in `api`, and Go's http client formats
	// failed requests as `Post "<url>": …` — so an unredacted error would
	// leak the token into logs AND the outbox lastError column. Scrub the
	// token from any error before it escapes this function.
	if err := d.postSync(ctx, api, body, nil); err != nil {
		return fmt.Errorf("%s", redact(err.Error()))
	}
	return nil
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
		"message":  plainSummary(e, 1024),
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
		plainSummary(e, 0) + "\r\n"

	var auth smtp.Auth
	if username != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}

	if err := smtpSendMail(ctx, host, port, auth, from, recipients, []byte(msg)); err != nil {
		// Context cancellation closes the conn mid-conversation, which
		// surfaces as an opaque I/O error — report the cancellation.
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		return fmt.Errorf("smtp: %w", err)
	}
	return nil
}

// smtpIsReservedIP guards the SMTP dial against reserved/private
// targets. It's an overridable seam so the tear-down tests (which must
// dial a loopback stub) can relax it; production always uses the shared
// httpx policy.
var smtpIsReservedIP = httpx.IsReservedIP

// smtpSendMail is smtp.SendMail with a context. The stdlib function
// takes none, so the previous shape ran it in a goroutine and selected
// on ctx — which returned early but LEAKED the goroutine + socket
// (SendMail kept blocking on a hung server), accumulating both across
// outbox retries. Here the dial is context-aware, the conn carries a
// hard deadline, and a watchdog closes the conn on cancellation so the
// whole SMTP conversation actually tears down.
func smtpSendMail(ctx context.Context, host, port string, auth smtp.Auth, from string, recipients []string, msg []byte) error {
	// Mirror smtp.SendMail's validateLine: addresses go verbatim into
	// MAIL FROM / RCPT TO lines, so CR/LF would inject commands.
	for _, line := range append([]string{from}, recipients...) {
		if strings.ContainsAny(line, "\r\n") {
			return fmt.Errorf("address contains CR/LF: %q", line)
		}
	}
	// SSRF defence-in-depth. The SMTP host is admin-supplied so this is
	// less exposed than the user-set HTTP webhook channels, but keep it
	// consistent with them: resolve the host and refuse to dial any IP
	// in a reserved/private range (loopback, link-local incl. cloud
	// metadata 169.254.169.254, RFC1918). We dial the *resolved* IP so
	// there's no check-then-dial rebinding gap, matching httpx's
	// SSRFSafeTransport policy. Every resolved IP must pass.
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	if len(ips) == 0 {
		return fmt.Errorf("smtp: no IPs for host %q", host)
	}
	for _, ip := range ips {
		if smtpIsReservedIP(ip) {
			return fmt.Errorf("smtp: refusing to dial reserved address %s (%s)", ip, host)
		}
	}
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(ips[0].String(), port))
	if err != nil {
		return err
	}
	// Hard ceiling on the whole conversation, even without a ctx
	// deadline — a server that accepts the dial then stalls mid-DATA
	// must not pin the outbox worker.
	deadline := time.Now().Add(60 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	// Watchdog: cancellation closes the conn so any blocked read/write
	// inside the smtp client returns immediately.
	watchdogDone := make(chan struct{})
	defer close(watchdogDone)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-watchdogDone:
		}
	}()

	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	// STARTTLS when the server offers it, plain otherwise — same
	// opportunistic policy smtp.SendMail applied.
	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
			return err
		}
	}
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range recipients {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	wc, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := wc.Write(msg); err != nil {
		_ = wc.Close()
		return err
	}
	if err := wc.Close(); err != nil {
		return err
	}
	return c.Quit()
}
