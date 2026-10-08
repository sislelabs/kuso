package notify

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// UptimeTarget is one service in an uptime event.
type UptimeTarget struct {
	Project, Service string
	// Reason is the failed check's error class (down events).
	Reason string
	// Since is the outage's first failed check.
	Since time.Time
	// DownFor is the outage length (recovered events).
	DownFor time.Duration
	// Closed, on a recovered event, says why the outage ended without
	// the service answering again ("stopped", "service deleted", …).
	Closed string
}

// UptimeDown: one project's services stopped answering this tick.
func UptimeDown(project string, targets []UptimeTarget) Event {
	e := Event{
		Type:     EventUptimeDown,
		Project:  project,
		Env:      "production",
		Severity: "error",
		Links:    []EventLink{{Label: "Project", URL: projectURL(project)}},
	}
	if len(targets) == 1 {
		t := targets[0]
		e.Service = t.Service
		e.Title = "✗ Down · " + Scope(project, t.Service, "")
		e.Description = "Not answering: " + t.Reason
		e.Body = t.Reason
		e.URL = ServiceLink(project, t.Service, "", "")
		e.Fields = []EventField{{Name: "Reason", Value: t.Reason, Inline: true}}
		if tok := TimeToken(t.Since); tok != "" {
			e.Fields = append(e.Fields, EventField{Name: "Since", Value: tok, Inline: true})
		}
		e.Links = []EventLink{
			{Label: "Logs", URL: ServiceLink(project, t.Service, "logs", "")},
			{Label: "Service", URL: ServiceLink(project, t.Service, "", "")},
		}
		return e
	}
	e.Title = fmt.Sprintf("✗ %d services down · %s", len(targets), project)
	e.Description = uptimeLines(targets, false, false)
	e.Body = e.Description
	e.URL = projectURL(project)
	return e
}

// UptimeRecovered: one project's services answer again.
func UptimeRecovered(project string, targets []UptimeTarget) Event {
	e := Event{
		Type:     EventUptimeRecovered,
		Project:  project,
		Env:      "production",
		Severity: "info",
	}
	if len(targets) == 1 {
		t := targets[0]
		e.Service = t.Service
		e.Title = "✓ Back up · " + Scope(project, t.Service, "")
		e.Description = "Was down for " + formatShortDuration(t.DownFor)
		if t.Closed != "" {
			e.Title = "◼ No longer checked · " + Scope(project, t.Service, "")
			e.Description = "Was down for " + formatShortDuration(t.DownFor) + ", then " + t.Closed + "."
		}
		e.Body = e.Description
		e.URL = ServiceLink(project, t.Service, "", "")
		return e
	}
	e.Title = fmt.Sprintf("✓ %d services back up · %s", len(targets), project)
	e.Description = uptimeLines(targets, false, true)
	e.Body = e.Description
	e.URL = projectURL(project)
	return e
}

// UptimeDownCluster: services in several projects went down in the same
// minute. It names every project, so it goes only to channels that see
// all projects; send the per-project events with AudienceScoped too.
func UptimeDownCluster(targets []UptimeTarget) Event {
	desc := uptimeLines(targets, true, false) +
		"\n\nSeveral projects failing in the same minute usually means a node or platform problem."
	return Event{
		Audience:    AudienceUnscoped,
		Type:        EventUptimeDown,
		Title:       fmt.Sprintf("✗ %d services down across %d projects", len(targets), uptimeProjects(targets)),
		Description: desc,
		Body:        desc,
		URL:         "/settings/nodes",
		Severity:    "error",
		Links:       []EventLink{{Label: "Nodes", URL: "/settings/nodes"}},
	}
}

// UptimeRecoveredCluster is UptimeDownCluster's counterpart.
func UptimeRecoveredCluster(targets []UptimeTarget) Event {
	desc := uptimeLines(targets, true, true)
	return Event{
		Audience:    AudienceUnscoped,
		Type:        EventUptimeRecovered,
		Title:       fmt.Sprintf("✓ %d services back up across %d projects", len(targets), uptimeProjects(targets)),
		Description: desc,
		Body:        desc,
		Severity:    "info",
	}
}

func uptimeProjects(targets []UptimeTarget) int {
	seen := map[string]struct{}{}
	for _, t := range targets {
		seen[t.Project] = struct{}{}
	}
	return len(seen)
}

// uptimeLines renders one line per target, capped so a large outage
// still fits a Discord embed.
func uptimeLines(targets []UptimeTarget, withProject, recovered bool) string {
	const maxLines = 20
	ts := append([]UptimeTarget(nil), targets...)
	sort.Slice(ts, func(i, j int) bool {
		if ts[i].Project != ts[j].Project {
			return ts[i].Project < ts[j].Project
		}
		return ts[i].Service < ts[j].Service
	})
	var b strings.Builder
	for i, t := range ts {
		if i == maxLines {
			fmt.Fprintf(&b, "…and %d more", len(ts)-maxLines)
			break
		}
		name := t.Service
		if withProject {
			name = Scope(t.Project, t.Service, "")
		}
		if recovered && t.Closed != "" {
			fmt.Fprintf(&b, "• %s (down for %s, then %s)\n", name, formatShortDuration(t.DownFor), t.Closed)
		} else if recovered {
			fmt.Fprintf(&b, "• %s (down for %s)\n", name, formatShortDuration(t.DownFor))
		} else {
			fmt.Fprintf(&b, "• %s: %s\n", name, t.Reason)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
