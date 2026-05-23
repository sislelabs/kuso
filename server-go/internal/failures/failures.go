// Package failures classifies build and pod terminal failures into a
// fixed set of kinds so the UI can deep-link the right overlay tab
// with a human summary instead of dumping the user on the canvas
// with a red ring and 300 lines of raw stderr to grep through.
//
// This is the "what just went wrong" path that runs once per failure
// event. It's intentionally NOT the streaming log scanner — that
// belongs to internal/errorscan, which pre-aggregates ERROR-level log
// lines for the alert engine and dashboard. failures.Classify reads
// the last N lines of the dead container/build pod's logs (plus the
// pod's terminated container status when available) and returns one
// Classification with the kind, the tab the UI should open on, a
// one-line human summary, and the offending log line for highlight.
//
// Kinds are an enum string, not an iota, because they cross the wire
// into the bell-popover JSON + the persisted NotificationEvent row,
// and a stable string is forgiving when consumers ship at different
// versions. Add new kinds at the bottom of the const block; existing
// ones do not change spelling.
package failures

import (
	"regexp"
	"strings"
)

// Kind enumerates the failure classes the UI knows how to route. The
// zero value KindGeneric is the fallback when no detector matches —
// the UI still opens the Logs tab; it just shows a generic banner.
type Kind string

const (
	KindGeneric            Kind = "generic"
	KindMissingEnv         Kind = "missing_env"
	KindOOM                Kind = "oom"
	KindCrashLoop          Kind = "crash_loop"
	KindImagePullFailed    Kind = "image_pull_failed"
	KindPortConflict       Kind = "port_conflict"
	KindHealthcheckFailed  Kind = "healthcheck_failed"
	KindBuildCommandFailed Kind = "build_command_failed"
)

// Tab is the overlay tab slug the UI should open on. Kept as a string
// for the same wire-stability reason Kind is. The web's ServiceOverlay
// already accepts ?tab=<slug>; we just need to populate the right one.
type Tab string

const (
	TabLogs      Tab = "logs"
	TabVariables Tab = "variables"
	TabSettings  Tab = "settings"
)

// Classification is the wire shape that travels with a failure event.
// All fields are populated by Classify; consumers can rely on Kind,
// Tab, and Summary being non-empty (Classify falls back to KindGeneric
// + a generic summary if no detector matches). LineHint is the raw
// log line that triggered the match — empty for kinds detected from
// pod status alone (OOM, ImagePullBackOff). LineNum is best-effort:
// the 1-based offset of LineHint inside the supplied logs slice;
// zero (omitted) when the kind didn't come from a log match.
type Classification struct {
	Kind     Kind   `json:"kind"`
	Tab      Tab    `json:"tab"`
	Summary  string `json:"summary"`
	LineHint string `json:"lineHint,omitempty"`
	LineNum  int    `json:"lineNum,omitempty"`
}

// Signal is the optional pod-status side-channel the caller can pass
// when a pod-watcher already has the terminated container's reason
// (CrashLoopBackOff, OOMKilled, ErrImagePull, ImagePullBackOff,
// CreateContainerConfigError). Build failures don't have these and
// pass an empty Signal. Both fields are strings to keep this package
// stdlib-only — no k8s.io imports — so the classifier can be unit-
// tested without faking a kube client.
type Signal struct {
	// Reason is the kubelet "waiting" or "terminated" reason. Examples:
	// "CrashLoopBackOff", "OOMKilled", "ErrImagePull",
	// "ImagePullBackOff", "CreateContainerConfigError".
	Reason string
	// ExitCode is the terminated container's exit code. 137 = SIGKILL
	// (often OOM); 139 = SIGSEGV; 0 = clean (won't be a failure event
	// in the first place). Zero value means the caller didn't have it
	// — Reason takes precedence.
	ExitCode int32
}

// Classify inspects pod-status signal + the last N log lines and
// returns a Classification. logLines should already be tail-sliced
// (the caller knows what "the relevant tail" looks like for its
// kind of failure — 5 lines for a Discord card, 50 for diagnostics).
//
// Detection order matters: more specific signals win over log regex
// matches. A pod with OOMKilled reason classifies as OOM even if its
// logs also contain "Address already in use" from an earlier boot.
func Classify(logLines []string, sig Signal) Classification {
	// Pod-status signals first — these are unambiguous when present.
	if c, ok := classifyFromSignal(sig); ok {
		return c
	}
	// Log-line regex matches second. Walk the tail in reverse so a
	// fresh failure outranks an older one repeated earlier in the
	// buffer — the most-recent line is the one that took the pod
	// down.
	for i := len(logLines) - 1; i >= 0; i-- {
		line := logLines[i]
		for _, d := range logDetectors {
			if d.re.MatchString(line) {
				return Classification{
					Kind:     d.kind,
					Tab:      d.tab,
					Summary:  d.summarize(line),
					LineHint: truncateLine(line),
					LineNum:  i + 1,
				}
			}
		}
	}
	return Classification{
		Kind:    KindGeneric,
		Tab:     TabLogs,
		Summary: "Deploy failed. See logs for details.",
	}
}

// classifyFromSignal maps kubelet container-state reasons to Kinds.
// Returns ok=false when the signal doesn't match any known reason;
// the caller then falls through to log-regex matching.
func classifyFromSignal(sig Signal) (Classification, bool) {
	switch sig.Reason {
	case "OOMKilled":
		return Classification{
			Kind:    KindOOM,
			Tab:     TabLogs,
			Summary: "Pod ran out of memory. Bump the memory request in Settings → Scale.",
		}, true
	case "CrashLoopBackOff":
		return Classification{
			Kind:    KindCrashLoop,
			Tab:     TabLogs,
			Summary: "Pod keeps crashing. See logs for the failing stack.",
		}, true
	case "ErrImagePull", "ImagePullBackOff":
		return Classification{
			Kind:    KindImagePullFailed,
			Tab:     TabSettings,
			Summary: "Couldn't pull the image. Check the registry credentials in Settings → Source.",
		}, true
	case "CreateContainerConfigError":
		// CreateContainerConfigError on a kuso pod almost always means
		// a referenced Secret/ConfigMap key is missing — i.e. an env
		// var the spec asked for isn't there. Route to Variables.
		return Classification{
			Kind:    KindMissingEnv,
			Tab:     TabVariables,
			Summary: "Container config error — a referenced env-var secret is missing.",
		}, true
	}
	// Exit-code-only fallback when Reason was blank (some kubelet
	// versions leave it empty on the terminated state). 137 means
	// SIGKILL which on Kubernetes is almost always the OOM-killer.
	if sig.Reason == "" && sig.ExitCode == 137 {
		return Classification{
			Kind:    KindOOM,
			Tab:     TabLogs,
			Summary: "Pod ran out of memory (SIGKILL). Bump memory in Settings → Scale.",
		}, true
	}
	return Classification{}, false
}

// logDetector pairs a regex with the Kind it implies plus a short
// per-kind summarizer that pulls a human one-liner out of the matched
// line. Detectors are tried in order; the first match wins.
type logDetector struct {
	kind      Kind
	tab       Tab
	re        *regexp.Regexp
	summarize func(line string) string
}

// logDetectors is the regex-driven detection list. Order is meaningful
// — more specific patterns come first. Keep this short: every line of
// every failed pod gets walked over it, so adding a slow regex here
// is a noticeable cost.
var logDetectors = []logDetector{
	// Missing env var — handled before the generic "Error:" catchers
	// because a missing-env error usually says ERROR somewhere too.
	{
		kind: KindMissingEnv,
		tab:  TabVariables,
		re:   regexp.MustCompile(`(?i)(missing|undefined|not set|required).{0,40}env|KeyError:.{0,40}env|environment variable .* (?:not set|required|missing)`),
		summarize: func(line string) string {
			if name := extractEnvName(line); name != "" {
				return "Missing env var: " + name
			}
			return "Missing env var — see logs for the variable name."
		},
	},
	// Port-conflict — happens when the container tries to listen on a
	// port already bound (commonly because the user changed the port
	// in settings but the prior process didn't release it).
	{
		kind: KindPortConflict,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)address already in use|EADDRINUSE|bind: address already in use`),
		summarize: func(line string) string {
			if p := extractPort(line); p != "" {
				return "Port " + p + " is already in use inside the container."
			}
			return "Container port is already in use."
		},
	},
	// Healthcheck — readiness/liveness failures echoed by some
	// frameworks ("Health check failed", "probe failed").
	{
		kind: KindHealthcheckFailed,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)(readiness|liveness) probe failed|health(?: ?check)? (?:failed|did not pass)`),
		summarize: func(line string) string {
			return "Health probe failing — pod isn't accepting traffic."
		},
	},
	// Build command failed — kaniko / nixpacks / buildpacks usually
	// echo "command failed with exit code N" or "Error: build failed".
	{
		kind: KindBuildCommandFailed,
		tab:  TabLogs,
		re:   regexp.MustCompile(`(?i)build failed|command failed with exit code|error building image|nixpacks build failed|buildpack failed`),
		summarize: func(line string) string {
			return "Build command exited non-zero. " + briefLine(line)
		},
	},
	// Crash-loop catch-all — Go/Python/Node panics. Last in the list
	// so more specific causes (missing env, port conflict) classify
	// first when both patterns match.
	{
		kind: KindCrashLoop,
		tab:  TabLogs,
		re:   regexp.MustCompile(`^panic:|Traceback \(most recent call last\)|UnhandledPromiseRejection|java\.lang\..*Exception`),
		summarize: func(line string) string {
			return "Process crashed: " + briefLine(line)
		},
	},
}

// extractEnvName pulls the env-var name out of common missing-env log
// shapes:
//
//	"Missing env var DATABASE_URL"
//	"KeyError: 'DATABASE_URL'"
//	"environment variable DATABASE_URL not set"
//
// Returns "" when no name is extractable. We accept ASCII identifier
// shapes (uppercase letters, digits, underscore) — env var names that
// don't match that convention exist but are rare and the cost of a
// false miss is just a less-specific summary string.
var envNameRE = regexp.MustCompile(`[A-Z][A-Z0-9_]{1,63}`)

func extractEnvName(line string) string {
	return envNameRE.FindString(line)
}

// extractPort pulls a TCP port number out of a port-conflict log line.
// Looks for "port N" or ":N" patterns. Returns "" when not found —
// the summary then omits the specific port and just says "the port".
var portRE = regexp.MustCompile(`port\s+(\d{2,5})|:(\d{2,5})\b`)

func extractPort(line string) string {
	m := portRE.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			return m[i]
		}
	}
	return ""
}

// truncateLine bounds the LineHint that travels in the notification
// payload. Log lines can be megabytes long (some structured loggers
// dump entire request bodies); we never want to push that through a
// webhook or store it on a NotificationEvent row.
const maxLineHint = 400

func truncateLine(s string) string {
	s = strings.TrimRight(s, "\r\n")
	if len(s) <= maxLineHint {
		return s
	}
	return s[:maxLineHint] + "…"
}

// briefLine is truncateLine but tighter, for embedding inside a
// summary string. The full line still travels in LineHint for the
// UI to render in a code block.
const maxBriefLine = 120

func briefLine(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxBriefLine {
		return s
	}
	return s[:maxBriefLine] + "…"
}
