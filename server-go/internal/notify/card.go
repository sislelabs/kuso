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
	"unicode"

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
		if strings.Trim(strings.TrimSpace(f.Value), "`") == body {
			return ""
		}
	}
	return body
}

// whySource says which part of the card a ping's reason line came from,
// so the embed can leave that line out instead of repeating it.
type whySource int

const (
	whyTitle whySource = iota
	whyDiagnosis
	whyDescription
	whyReason
)

// pingReason is the one-line plain-text "what broke" that follows a ping:
// the diagnosis when kuso has one, else the first line of the description,
// else the Reason field, else the title. It's the message content Discord
// shows in a push notification, so it carries no markdown.
func pingReason(e Event) (string, whySource) {
	plain := func(s string) string {
		s = discordToMarkup(expandTimes(s, markupPlain), markupPlain)
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[:i]
		}
		return truncateRunes(strings.Trim(strings.TrimSpace(s), "`"), 160)
	}
	if c := e.Classification; c != nil && c.Kind != failures.KindGeneric {
		if why := plain(c.Summary); why != "" {
			return why, whyDiagnosis
		}
	}
	if why := plain(cardDescription(e)); why != "" {
		return why, whyDescription
	}
	for _, f := range e.Fields {
		if f.Name == "Reason" {
			if why := plain(f.Value); why != "" {
				return why, whyReason
			}
		}
	}
	return e.Title, whyTitle
}

// dropFirstLine removes s's first line (the one a ping already showed).
func dropFirstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return ""
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
func diagnosisText(c *failures.Classification, m markup, skipSummary bool) string {
	if c == nil || c.Kind == failures.KindGeneric {
		return ""
	}
	summary := strings.TrimSpace(c.Summary)
	if summary == "" {
		return ""
	}
	var b strings.Builder
	if !skipSummary {
		b.WriteString("💡 " + bold(summary, m))
	}
	if r := c.Remediation; r != nil {
		fix := strings.TrimSpace(r.Title)
		if fix == "" {
			fix = strings.TrimSpace(r.Detail)
		}
		if fix != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			} else {
				b.WriteString("💡 ")
			}
			b.WriteString(bold("Fix:", m) + " " + fix)
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

// factsLine folds inline fields into one label-less line: "`OOMKilled` ·
// 5 restarts · since 3m ago". Inline values must read on their own; the
// Name only survives for raw-webhook consumers.
func factsLine(fields []EventField, m markup) string {
	parts := make([]string, 0, len(fields))
	for _, f := range fields {
		v := strings.TrimSpace(f.Value)
		if !f.Inline || v == "" {
			continue
		}
		// Multi-line values (several Site links) join on one line too.
		parts = append(parts, discordToMarkup(strings.ReplaceAll(v, "\n", ", "), m))
	}
	return strings.Join(parts, " · ")
}

// labeledLines renders full-width fields as "**Name** value", one per
// line, so they take a line each instead of a header row plus a value.
func labeledLines(fields []EventField, m markup) string {
	lines := make([]string, 0, len(fields))
	for _, f := range fields {
		v := strings.TrimSpace(f.Value)
		if f.Inline || f.Name == "" || v == "" {
			continue
		}
		lines = append(lines, bold(f.Name, m)+" "+discordToMarkup(v, m))
	}
	return strings.Join(lines, "\n")
}

// discordLogLines is how many trailing log lines a Discord card shows:
// enough to see the failing line without the card filling the screen.
const discordLogLines = 6

// logLineMaxRunes caps one log line; minified stack traces otherwise
// turn six lines into a wall.
const logLineMaxRunes = 180

// tailLines keeps the last n non-blank lines of a log, each capped.
func tailLines(s string, n int) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l = strings.TrimRight(l, " \t\r"); strings.TrimSpace(l) != "" {
			lines = append(lines, truncateRunes(l, logLineMaxRunes))
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// proseMaxLines / proseMaxRunes bound a card's description. Anything
// longer belongs in the dashboard the title links to.
const (
	proseMaxLines = 3
	proseMaxRunes = 320
)

// listMaxRunes caps a bulleted description (down services, new CVEs):
// there the rows ARE the information, so it isn't cut by lines.
const listMaxRunes = 900

// shortProse trims a description to its first few lines. A bulleted
// list keeps its rows (the emit site caps how many); a description
// carrying a code fence is only rune-capped, since cutting it by lines
// could leave the fence open.
func shortProse(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if isBulleted(s) {
		return truncateRunes(s, listMaxRunes)
	}
	if !strings.Contains(s, "```") {
		var keep []string
		for _, l := range strings.Split(s, "\n") {
			if strings.TrimSpace(l) == "" {
				continue
			}
			if len(keep) == proseMaxLines {
				keep[len(keep)-1] = strings.TrimRight(keep[len(keep)-1], ".…") + "…"
				break
			}
			keep = append(keep, l)
		}
		s = strings.Join(keep, "\n")
		return truncateRunes(s, proseMaxRunes)
	}
	return truncateRunes(s, proseMaxRunes*2)
}

// isBulleted: more than one line starts with "• ".
func isBulleted(s string) bool {
	n := 0
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "• ") {
			n++
		}
	}
	return n > 1
}

// cardTitle is the event title with a status glyph in front. Most emit
// sites write one ("✗ Crashing · …"); the few that don't get one from
// the event's tone so every card in a channel scans the same way.
func cardTitle(e Event) string {
	t := strings.TrimSpace(e.Title)
	if t == "" {
		return t
	}
	if r := []rune(t)[0]; unicode.IsLetter(r) || unicode.IsDigit(r) {
		switch eventTone(e) {
		case toneError:
			return "✗ " + t
		case toneWarn:
			return "⚠ " + t
		case toneSuccess:
			return "✓ " + t
		case toneNeutral:
			return "⊘ " + t
		}
	}
	return t
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
