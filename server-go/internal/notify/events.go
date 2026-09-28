package notify

// events.go: constructors for the events notify owns end to end
// (crashes, recovery, nodes, runs, alerts). Title grammar everywhere:
//
//	<glyph> <What> · <project> / <service> → <env>
//
// glyphs: ✓ ok · ✗ failure · ⚠ warning · ⊘ stopped · ▶ started.

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"kuso/server/internal/failures"
)

// Scope renders "<project> / <service> → <env>", dropping empty parts.
func Scope(project, service, env string) string {
	s := project
	if service != "" {
		if s != "" {
			s += " / "
		}
		s += service
	}
	if env != "" {
		s += " → " + env
	}
	return s
}

// EnvSeverity maps a failure's severity by environment: production (or
// an unknown env) keeps sev; any other env is capped at warn. The
// default @here goes to error-severity events, and a broken PR preview
// paging the whole channel trained people to mute it.
func EnvSeverity(env, sev string) string {
	if env == "" || env == "production" {
		return sev
	}
	if normalizeSeverity(sev) == "error" {
		return "warn"
	}
	return sev
}

// ServiceLink is the in-app service overlay path, optionally pinned to
// a tab and an env group. "" without project + service.
func ServiceLink(project, service, tab, env string) string {
	u := serviceURL(project, service)
	if u == "" {
		return ""
	}
	if tab != "" {
		u += "&tab=" + url.QueryEscape(tab)
	}
	if env != "" {
		u += "&env=" + url.QueryEscape(env)
	}
	return u
}

// PodCrash describes one crash episode for PodCrashed.
type PodCrash struct {
	Project, Service, Env string
	Pod, Reason, LogTail  string
	Restarts              int
	// Since is when the episode started (first observed); zero = unknown.
	Since          time.Time
	Classification *failures.Classification
}

// PodCrashed: a service's pods are crashlooping / can't pull / can't
// start. Production crashes are error severity (default @here); other
// envs are warn. The classifier's diagnosis renders on the card.
func PodCrashed(c PodCrash) Event {
	// The classifier doesn't know the service, so its fix commands carry
	// <project> <service> placeholders; fill them so the card's command
	// can be pasted as-is. Copy first: the caller owns the classification.
	if cl := c.Classification; cl != nil && cl.Remediation != nil && c.Project != "" && c.Service != "" {
		rem := *cl.Remediation
		rem.Fix = strings.ReplaceAll(rem.Fix, "<project> <service>", c.Project+" "+c.Service)
		cp := *cl
		cp.Remediation = &rem
		c.Classification = &cp
	}
	fields := []EventField{{Name: "Reason", Value: c.Reason, Inline: true}}
	if c.Restarts > 0 {
		fields = append(fields, EventField{Name: "Restarts", Value: strconv.Itoa(c.Restarts), Inline: true})
	}
	if tok := TimeToken(c.Since); tok != "" {
		fields = append(fields, EventField{Name: "Since", Value: tok, Inline: true})
	}
	link := serviceURL(c.Project, c.Service)
	if link != "" && c.Classification != nil && c.Classification.Tab != "" {
		link += "&tab=" + string(c.Classification.Tab) + "&kind=" + string(c.Classification.Kind)
		if c.Classification.LineNum > 0 {
			link += "&highlight=" + strconv.Itoa(c.Classification.LineNum)
		}
	}
	if link != "" && c.Env != "" {
		link += "&env=" + url.QueryEscape(c.Env)
	}
	return Event{
		Type:           EventPodCrashed,
		Title:          "✗ Crashing · " + Scope(c.Project, c.Service, c.Env),
		LogTail:        c.LogTail,
		Body:           c.Reason, // raw-webhook consumers
		Project:        c.Project,
		Service:        c.Service,
		Env:            c.Env,
		URL:            link,
		Severity:       EnvSeverity(c.Env, "error"),
		Extra:          map[string]string{"pod": c.Pod},
		Fields:         fields,
		Classification: c.Classification,
		Links: []EventLink{
			{Label: "Logs", URL: ServiceLink(c.Project, c.Service, "logs", c.Env)},
			{Label: "Service", URL: ServiceLink(c.Project, c.Service, "", c.Env)},
		},
	}
}

// PodRecovered closes a pod.crashed episode once the env has stayed
// healthy for the watcher's stability window. down is the episode
// length (first crash → healthy); 0 = unknown.
func PodRecovered(project, service, env string, down time.Duration) Event {
	desc := "Healthy again"
	if down > 0 {
		desc = "Healthy again after " + formatShortDuration(down)
	}
	return Event{
		Type:        EventPodRecovered,
		Title:       "✓ Recovered · " + Scope(project, service, env),
		Description: desc,
		Body:        desc,
		Project:     project,
		Service:     service,
		Env:         env,
		URL:         ServiceLink(project, service, "", env),
		Severity:    "info",
	}
}

// AddonCrashed: a managed addon's pod is crashlooping. Every service
// mounting the addon's conn secret loses its datastore, so it's error
// severity regardless of env.
func AddonCrashed(project, addon, addonKind, podName, reason, logTail string, restarts int) Event {
	name := addon
	if addonKind != "" {
		name += " (" + addonKind + ")"
	}
	fields := []EventField{{Name: "Reason", Value: reason, Inline: true}}
	if restarts > 0 {
		fields = append(fields, EventField{Name: "Restarts", Value: strconv.Itoa(restarts), Inline: true})
	}
	return Event{
		Type:     EventAddonCrashed,
		Title:    "✗ Addon crashing · " + Scope(project, name, ""),
		LogTail:  logTail,
		Body:     reason,
		Project:  project,
		URL:      projectURL(project), // the addon node lives on the canvas
		Severity: "error",
		Extra:    map[string]string{"pod": podName, "addon": addon, "addonKind": addonKind},
		Fields:   fields,
		Links:    []EventLink{{Label: "Project", URL: projectURL(project)}},
	}
}

// NodeUnreachable: a node has been NotReady past nodewatch's threshold
// and was auto-cordoned. downFor is how long it's been NotReady at emit
// time; affectedPods is 0 when unknown.
func NodeUnreachable(node, reason string, downFor time.Duration, affectedPods int) Event {
	desc := "NotReady — auto-cordoned, new pods won't schedule here."
	if downFor > 0 {
		desc = "NotReady since " + TimeToken(time.Now().Add(-downFor)) + " — auto-cordoned, new pods won't schedule here."
	}
	var fields []EventField
	if reason != "" {
		fields = append(fields, EventField{Name: "Reason", Value: reason, Inline: true})
	}
	if affectedPods > 0 {
		fields = append(fields, EventField{Name: "Pods on node", Value: strconv.Itoa(affectedPods), Inline: true})
	}
	return Event{
		Type:        EventNodeUnreachable,
		Title:       "✗ Node unreachable · " + node,
		Description: desc,
		Body:        reason,
		URL:         "/settings/nodes",
		Severity:    "error",
		Extra:       map[string]string{"node": node},
		Fields:      fields,
		Links:       []EventLink{{Label: "Nodes", URL: "/settings/nodes"}},
	}
}

// NodeRecovered: a node nodewatch cordoned is Ready again and was
// uncordoned. downFor is the total outage (0 = unknown).
func NodeRecovered(node string, downFor time.Duration) Event {
	desc := "Ready again — uncordoned"
	if downFor > 0 {
		desc = "Ready again after " + formatShortDuration(downFor) + " — uncordoned"
	}
	return Event{
		Type:        EventNodeRecovered,
		Title:       "✓ Node recovered · " + node,
		Description: desc,
		Body:        desc,
		URL:         "/settings/nodes",
		Severity:    "info",
		Extra:       map[string]string{"node": node},
	}
}

func runCommand(command []string) string {
	cmd := strings.Join(command, " ")
	if len(cmd) > 200 {
		cmd = cmd[:200] + "…"
	}
	return "`" + cmd + "`"
}

func runFields(runName string, durationMs int64) []EventField {
	fields := []EventField{{Name: "Run", Value: "`" + runName + "`", Inline: true}}
	if durationMs > 0 {
		fields = append(fields, EventField{
			Name: "Took", Value: formatShortDuration(time.Duration(durationMs) * time.Millisecond), Inline: true,
		})
	}
	return fields
}

// RunStarted: a KusoRun was created. The poller fires RunSucceeded /
// RunFailed at the terminal transition.
func RunStarted(project, service, runName string, command []string, triggeredByUser string) Event {
	by := triggeredByUser
	if by == "" {
		by = "system"
	}
	return Event{
		Type:        EventRunStarted,
		Title:       "▶ Run started · " + Scope(project, service, ""),
		Description: runCommand(command),
		Project:     project,
		Service:     service,
		URL:         runEventURL(project, service),
		Severity:    "info",
		Fields: []EventField{
			{Name: "Run", Value: "`" + runName + "`", Inline: true},
			{Name: "By", Value: by, Inline: true},
		},
	}
}

// RunSucceeded: the run's Job completed. durationMs 0 = unknown.
func RunSucceeded(project, service, runName string, command []string, durationMs int64) Event {
	return Event{
		Type:        EventRunSucceeded,
		Title:       "✓ Run succeeded · " + Scope(project, service, ""),
		Description: runCommand(command),
		Project:     project,
		Service:     service,
		URL:         runEventURL(project, service),
		Severity:    "info",
		Fields:      runFields(runName, durationMs),
		DurationMs:  durationMs,
	}
}

// RunFailed: the run's Job failed. message is the Job's failure
// condition (typically the kubelet reason + exit code).
func RunFailed(project, service, runName string, command []string, message string, durationMs int64) Event {
	desc := runCommand(command)
	if message != "" {
		desc += "\n" + message
	}
	return Event{
		Type:        EventRunFailed,
		Title:       "✗ Run failed · " + Scope(project, service, ""),
		Description: desc,
		Body:        message,
		Project:     project,
		Service:     service,
		URL:         runEventURL(project, service),
		Severity:    "error",
		Fields:      runFields(runName, durationMs),
		DurationMs:  durationMs,
		Links: []EventLink{
			{Label: "Runs", URL: runEventURL(project, service)},
			{Label: "Logs", URL: ServiceLink(project, service, "logs", "")},
		},
	}
}

// AlertFired builds an alert.fired event. name is the rule / condition
// name (rendered as "⚠ Alert · <name>"); extra["project"] /
// extra["service"] scope it (deep-link, mute, channel project filter).
func AlertFired(name, body, severity string, extra map[string]string) Event {
	project, service := extra["project"], extra["service"]
	glyph := "⚠"
	if normalizeSeverity(severity) == "error" {
		glyph = "✗"
	}
	title := glyph + " Alert · " + name
	if sc := Scope(project, service, ""); sc != "" {
		title += " · " + sc
	}
	links := []EventLink{}
	if u := AlertURL(project, service); u != "/settings/alerts" {
		links = append(links, EventLink{Label: "Open", URL: u})
	}
	links = append(links, EventLink{Label: "Alert rules", URL: "/settings/alerts"})
	return Event{
		Type:     EventAlertFired,
		Title:    title,
		Body:     body,
		Project:  project,
		Service:  service,
		URL:      AlertURL(project, service),
		Severity: severity,
		Extra:    extra,
		Links:    links,
	}
}

// AlertResolved closes an alert episode opened by AlertFired. It keeps
// the alert.fired type so every channel that received the fire also
// gets the all-clear; Extra["state"]="resolved" tells webhook consumers
// which edge this is.
func AlertResolved(name, body string, extra map[string]string) Event {
	ev := AlertFired(name, body, "info", extra)
	ev.Title = "✓ Resolved · " + strings.TrimPrefix(ev.Title, "⚠ Alert · ")
	if ev.Extra == nil {
		ev.Extra = map[string]string{}
	}
	ev.Extra["state"] = "resolved"
	return ev
}
