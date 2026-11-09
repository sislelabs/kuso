package kusoCli

import (
	"strings"
	"time"
)

// The env status vocabulary, kept identical to the web's
// (web/src/lib/status.ts) so the CLI never calls a state "running" where
// the dashboard says "Live". The raw server state (status.state) stays in
// -o json.

// serviceStatusLabel maps env status.state onto the web's labels.
// Unrecognised states pass through so a newer server is still readable.
func serviceStatusLabel(state string) string {
	switch state {
	case "":
		return ""
	case "running":
		return "Live"
	case "deploying":
		return "Deploying"
	case "building":
		return "Building"
	case "degraded", "release_failed":
		return "Degraded"
	case "crashlooping", "build_failed":
		return "Failing"
	case "stuck":
		return "Stuck"
	case "rollout_failing":
		return "Rollout failing"
	case "sleeping":
		return "Sleeping"
	case "stopped":
		return "Stopped"
	case "no_image":
		return "Not deployed"
	case "unknown":
		return "Unknown"
	}
	return state
}

// serverStateCause names the cause a label hides: crashloops and failed
// builds are both "Failing". Mirrors serverStateCause in status.ts.
func serverStateCause(state string) string {
	switch state {
	case "crashlooping":
		return "Crashlooping"
	case "build_failed":
		return "Latest build failed"
	case "release_failed":
		return "Release hook failed"
	case "stuck":
		return "Can't start"
	}
	return ""
}

// envStatusReason is the one-line plain-language reason for a state that
// needs attention: cause, then the server's stateDetail. "" when the env
// is plainly live or there's nothing to say.
func envStatusReason(status map[string]any) string {
	state := asString(status["state"])
	if state == "" || state == "running" {
		return ""
	}
	detail := asString(status["stateDetail"])
	cause := serverStateCause(state)
	switch {
	case cause != "" && detail != "":
		return cause + ": " + detail
	case cause != "":
		return cause
	}
	return capitalize(detail)
}

// envStatusFacts are the extra lines behind a failing rollout or a stuck
// env: when it started, how long the previous version has served, the
// health-check probe result and the raw kubelet/scheduler text.
func envStatusFacts(status map[string]any, now time.Time) []string {
	var out []string
	if r, ok := status["rollout"].(map[string]any); ok && asString(status["state"]) == "rollout_failing" {
		var parts []string
		if since := asString(r["since"]); since != "" {
			parts = append(parts, "new version failing for "+ageSince(since, now))
		}
		if b, _ := r["previousServing"].(bool); b {
			p := "previous version serving"
			if ps := asString(r["previousSince"]); ps != "" {
				p += " (live " + ageSince(ps, now) + ")"
			}
			parts = append(parts, p)
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, "; "))
		}
		// The cause already reads "GET /path → 404" once the probe result is known.
		if path := asString(r["healthcheckPath"]); path != "" && asString(r["probeResult"]) == "" {
			out = append(out, "health check GET "+path)
		}
	}
	if s, ok := status["stuck"].(map[string]any); ok {
		var parts []string
		if reason := asString(s["reason"]); reason != "" {
			parts = append(parts, "reason "+reason)
		}
		if since := asString(s["since"]); since != "" {
			parts = append(parts, "for "+ageSince(since, now))
		}
		if len(parts) > 0 {
			out = append(out, strings.Join(parts, ", "))
		}
		if msg := asString(s["message"]); msg != "" && msg != asString(s["summary"]) {
			out = append(out, truncate(oneLine(msg), 200))
		}
	}
	return out
}

func ageSince(iso string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return ageBetween(t, now)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// envStatusShortReason is envStatusReason for a table cell: a failing
// rollout's cause plus "previous version serving", so the part that says
// the app is still up survives truncation.
func envStatusShortReason(status map[string]any) string {
	if asString(status["state"]) == "rollout_failing" {
		if r, ok := status["rollout"].(map[string]any); ok && asString(r["cause"]) != "" {
			out := capitalize(asString(r["cause"]))
			if b, _ := r["previousServing"].(bool); b {
				out = "Previous version serving; " + asString(r["cause"])
			}
			return truncate(out, 100)
		}
	}
	return truncate(envStatusReason(status), 100)
}
