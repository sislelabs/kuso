package errorscan

import (
	"regexp"
	"strings"
	"time"
)

// An error group is named and fingerprinted after its title. The app's
// own log prefix (timestamp, level) is noise there, and a Python
// traceback cut before its final line used to be titled "Traceback (most
// recent call last):", which names nothing.
var (
	leadingTSRe    = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:[.,]\d+)?)?(?:Z|[+-]\d{2}:?\d{2})?\]?\s*`)
	leadingLevelRe = regexp.MustCompile(`(?i)^\[?(?:ERROR|ERR|FATAL|PANIC|CRIT(?:ICAL)?|SEVERE)\]?(?::\s*|\s+)`)
)

// canonicalTitle is the title stored as an error group's message and
// hashed into its fingerprint.
func canonicalTitle(title string, lines []string) string {
	t := stripLogPrefix(title)
	if strings.HasPrefix(t, "Traceback (most recent call last)") {
		t = stripLogPrefix(pythonFinalLine(lines))
	}
	return t
}

func stripLogPrefix(s string) string {
	t := strings.TrimSpace(s)
	stripped := strings.TrimSpace(leadingLevelRe.ReplaceAllString(leadingTSRe.ReplaceAllString(t, ""), ""))
	if stripped == "" {
		return t
	}
	return stripped
}

// pythonFinalLine is the last exception line of a traceback ("ValueError:
// bad input"), or a generic name when the trace was cut before it.
func pythonFinalLine(lines []string) string {
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimRight(lines[i], " ")
		if l == "" || l != strings.TrimLeft(l, " \t") || tracebackRe.MatchString(l) || strings.HasPrefix(l, "File \"") || pyChainedRe.MatchString(l) {
			continue
		}
		if pyFinalRe.MatchString(l) {
			return l
		}
	}
	return "Python traceback (exception line not captured)"
}

// titleFromRaw re-derives the canonical title of a stored occurrence from
// its rawLine, the way the scanner would name it today.
func titleFromRaw(raw string) string {
	lines := strings.Split(raw, "\n")
	var first *errorChain
	g := newGrouper(func(c *errorChain) {
		if first == nil {
			first = c
		}
	})
	ts := time.Unix(0, 0)
	for i, l := range lines {
		// The head was an error when it was stored, whatever the
		// patterns say now.
		g.feed(scanLine{pod: "p", ts: ts, line: l}, i == 0 || matchesAnyPattern(l))
	}
	g.flush()
	if first == nil {
		return canonicalTitle(lines[0], lines)
	}
	return canonicalTitle(first.title, first.lines)
}
