package notify

// card.go holds the per-channel building blocks every renderer shares:
// the link row, the diagnosis block, portable timestamps, the compact
// success layout and the instance identity (footer host + avatar).

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kuso/server/internal/failures"
)

// EventLink is one entry in a card's link row ("Logs · Deployments ·
// Site"). URL may be an in-app path; renderers upgrade it via
// absoluteURL and drop links that can't be made absolute. Discord
// webhooks can't carry real buttons, so this is the button row.
type EventLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// markup selects how a renderer spells links, bold and timestamps.
type markup int

const (
	markupDiscord markup = iota
	markupSlack
	markupPlain
)

// TimeToken embeds t in any Description or Field value. Each renderer
// expands it to its native form: Discord shows a live relative time in
// the reader's own timezone ("3 hours ago"), Slack a localized date,
// text channels an absolute UTC time. Zero t yields "".
func TimeToken(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return fmt.Sprintf("{{t:%d}}", t.Unix())
}

var timeTokenRE = regexp.MustCompile(`\{\{t:(-?\d+)\}\}`)

func expandTimes(s string, m markup) string {
	if !strings.Contains(s, "{{t:") {
		return s
	}
	return timeTokenRE.ReplaceAllStringFunc(s, func(tok string) string {
		n, err := strconv.ParseInt(timeTokenRE.FindStringSubmatch(tok)[1], 10, 64)
		if err != nil {
			return tok
		}
		abs := time.Unix(n, 0).UTC().Format("2006-01-02 15:04 UTC")
		switch m {
		case markupDiscord:
			return fmt.Sprintf("<t:%d:R>", n)
		case markupSlack:
			return fmt.Sprintf("<!date^%d^{date_short_pretty} {time}|%s>", n, abs)
		default:
			return abs
		}
	})
}

// cardDescription is the prose under the title: Description, else the
// legacy Body — unless Body just repeats a field (PodCrashed keeps the
// reason in Body for raw webhooks and in the Reason field for cards).
func cardDescription(e Event) string {
	if d := strings.TrimSpace(e.Description); d != "" {
		return d
	}
	body := strings.TrimSpace(e.Body)
	for _, f := range e.Fields {
		if strings.TrimSpace(f.Value) == body {
			return ""
		}
	}
	return body
}

// pushLine is the one-line plain-text summary of an event: the title,
// plus what's wrong when kuso knows — the diagnosis, else the first line
// of the description or body. It's the message content Discord shows in
// a push notification, so it carries no markdown.
func pushLine(e Event) string {
	why := ""
	if c := e.Classification; c != nil && c.Kind != failures.KindGeneric {
		why = strings.TrimSpace(c.Summary)
	}
	if why == "" {
		why = cardDescription(e)
	}
	if why == "" {
		for _, f := range e.Fields {
			if f.Name == "Reason" {
				why = f.Value
			}
		}
	}
	why = discordToMarkup(expandTimes(why, markupPlain), markupPlain)
	if i := strings.IndexByte(why, '\n'); i >= 0 {
		why = why[:i]
	}
	why = strings.Trim(strings.TrimSpace(why), "`")
	line := e.Title
	if why != "" && !strings.Contains(e.Title, why) {
		line += " — " + truncateRunes(why, 160)
	}
	return line
}

// bold renders s in the channel's bold syntax (none for plain text).
func bold(s string, m markup) string {
	switch m {
	case markupDiscord:
		return "**" + s + "**"
	case markupSlack:
		return "*" + s + "*"
	default:
		return s
	}
}

// discordToMarkup rewrites the Discord-flavoured markdown emit sites
// write (**bold**, [label](url)) for other channels. Code spans and
// fences are valid in all three and pass through.
var mdLinkRE = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)

func discordToMarkup(s string, m markup) string {
	switch m {
	case markupSlack:
		s = mdLinkRE.ReplaceAllString(s, "<$2|$1>")
		return strings.ReplaceAll(s, "**", "*")
	case markupPlain:
		s = mdLinkRE.ReplaceAllString(s, "$1 ($2)")
		return strings.ReplaceAll(s, "**", "")
	}
	return s
}

// diagnosisText renders the failure classifier's verdict: what kuso
// thinks went wrong and, when it knows one, the fix. Before this the
// classification was computed for every crash/build failure but only
// the web UI showed it — the Discord card made you open the logs to
// learn "out of memory". Generic classifications ("see logs") add
// nothing over the log tail, so they render as "".
func diagnosisText(c *failures.Classification, m markup) string {
	if c == nil || c.Kind == failures.KindGeneric {
		return ""
	}
	summary := strings.TrimSpace(c.Summary)
	if summary == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("💡 " + bold(summary, m))
	if r := c.Remediation; r != nil {
		fix := strings.TrimSpace(r.Title)
		if fix == "" {
			fix = strings.TrimSpace(r.Detail)
		}
		if fix != "" {
			b.WriteString("\n" + bold("Fix:", m) + " " + fix)
		}
		// Only short snippets: a card is for triage, the full
		// remediation lives in the dashboard.
		if snippet := strings.TrimSpace(r.Fix); snippet != "" && len(snippet) <= 400 && m != markupPlain {
			b.WriteString("\n```" + r.FixLang + "\n" + snippet + "\n```")
		} else if snippet != "" && len(snippet) <= 400 {
			b.WriteString("\n" + snippet)
		}
	}
	return b.String()
}

// linksLine renders e.Links as one row. Links whose URL can't be made
// absolute are skipped (Discord 400s relative links); "" when none.
func linksLine(e Event, m markup) string {
	parts := make([]string, 0, len(e.Links))
	for _, l := range e.Links {
		abs := absoluteURL(l.URL)
		if abs == "" || l.Label == "" {
			continue
		}
		switch m {
		case markupDiscord:
			parts = append(parts, "["+l.Label+"]("+abs+")")
		case markupSlack:
			parts = append(parts, "<"+abs+"|"+l.Label+">")
		default:
			parts = append(parts, l.Label+": "+abs)
		}
	}
	if m == markupPlain {
		return strings.Join(parts, "\n")
	}
	return strings.Join(parts, " · ")
}

// isCompact: success and neutral outcomes (build succeeded, run
// succeeded, recovered, cancelled) render as a short card with the
// fields folded into one line, so failures stand out in a busy channel.
func isCompact(e Event) bool {
	switch eventTone(e) {
	case toneSuccess, toneNeutral:
		return true
	}
	return false
}

// inlineFields folds fields into "**Name** value · **Name** value".
func inlineFields(fields []EventField, m markup) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Name == "" || f.Value == "" {
			continue
		}
		// Multi-line values (several Site links) join on one line too.
		v := strings.ReplaceAll(f.Value, "\n", ", ")
		parts = append(parts, bold(f.Name, m)+" "+v)
	}
	return strings.Join(parts, " · ")
}

// publicBase is the dashboard's base URL (no trailing slash), "" when
// the install doesn't know its own public address.
func publicBase() string {
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("KUSO_PUBLIC_URL")), "/")
	if base == "" {
		if d := strings.TrimSpace(os.Getenv("KUSO_DOMAIN")); d != "" {
			base = "https://" + d
		}
	}
	return base
}

// instanceHost names the kuso install in card footers, so a channel fed
// by several clusters shows which one posted.
func instanceHost() string {
	base := publicBase()
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil && u.Host != "" {
		return u.Host
	}
	return base
}

// avatarURL is the webhook message avatar (web/public/kuso-avatar.png,
// served from the embedded dashboard bundle). Discord only accepts
// raster images, hence a PNG and not the SVG logo.
func avatarURL() string {
	if base := publicBase(); base != "" {
		return base + "/kuso-avatar.png"
	}
	return ""
}
