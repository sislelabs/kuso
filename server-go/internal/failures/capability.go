package failures

import (
	"regexp"
	"strconv"
	"strings"
)

// Capability detection is a function rather than a logDetector: the
// caps in the fix depend on WHICH operation was denied, and the bind
// case needs a numeric port check a regex can't express cleanly.

var (
	// eperm is the errno text for a capability-denied syscall. EACCES is
	// only accepted for bind/listen, where it's how a low-port bind fails.
	epermRE = regexp.MustCompile(`(?i)operation not permitted|\bEPERM\b`)
	eaccRE  = regexp.MustCompile(`(?i)permission denied|\bEACCES\b`)

	chownRE  = regexp.MustCompile(`(?i)\bl?chown\b`)
	userRE   = regexp.MustCompile(`(?i)\b(?:setuid|setgid|setgroups|setresuid|setresgid|initgroups|gosu|su-exec|setpriv)\b|failed switching to`)
	capRE    = regexp.MustCompile(`(?i)\bcap_\w+`)
	bindRE   = regexp.MustCompile(`(?i)\bbind\b|\blisten\b`)
	lowPort  = regexp.MustCompile(`:(\d{1,5})\b`)
	buildkit = regexp.MustCompile(`^#\d+ `)
)

// capsForLine returns the capabilities a single log line shows missing,
// in a stable order, or nil when the line isn't a capability failure.
func capsForLine(line string) []string {
	if buildkit.MatchString(line) {
		return nil
	}
	var caps []string
	if epermRE.MatchString(line) {
		if chownRE.MatchString(line) {
			caps = append(caps, "CHOWN")
		}
		if userRE.MatchString(line) {
			caps = append(caps, "SETUID", "SETGID")
		}
		if len(caps) == 0 && capRE.MatchString(line) {
			caps = append(caps, "SETPCAP")
		}
	}
	if bindRE.MatchString(line) && (epermRE.MatchString(line) || eaccRE.MatchString(line)) && bindsPrivilegedPort(line) {
		caps = append(caps, "NET_BIND_SERVICE")
	}
	return caps
}

func bindsPrivilegedPort(line string) bool {
	for _, m := range lowPort.FindAllStringSubmatch(line, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > 0 && n < 1024 {
			return true
		}
	}
	return false
}

// classifyMissingCapability scans the whole tail: an image usually dies
// on the first denied op, but when several show up the fix should add
// all of them at once rather than one crash per round-trip.
func classifyMissingCapability(lines []string) (Classification, bool) {
	seen := map[string]bool{}
	var caps []string
	hint := -1
	for i, l := range lines {
		lc := capsForLine(l)
		if len(lc) == 0 {
			continue
		}
		hint = i
		for _, c := range lc {
			if !seen[c] {
				seen[c] = true
				caps = append(caps, c)
			}
		}
	}
	if hint < 0 {
		return Classification{}, false
	}
	flags := make([]string, len(caps))
	for i, c := range caps {
		flags[i] = "--cap-add " + c
	}
	return Classification{
		Kind:     KindMissingCapability,
		Tab:      TabLogs,
		Summary:  "The image needs Linux capabilities kuso drops by default.",
		LineHint: truncateLine(lines[hint]),
		LineNum:  hint + 1,
		Remediation: &Remediation{
			Title: "Add back " + strings.Join(caps, ", ") + " for this service",
			Detail: "kuso drops every Linux capability from app containers. This image starts as root and " +
				"then chowns files, switches user, or binds a port below 1024, and that call was denied. " +
				"Add back only the capabilities shown. --cap-add replaces the service's list, so include any " +
				"it already has. If the next start fails on a different operation, add that one too.",
			Fix:        "kuso project service set <project> <service> " + strings.Join(flags, " "),
			FixLang:    "bash",
		},
	}, true
}
