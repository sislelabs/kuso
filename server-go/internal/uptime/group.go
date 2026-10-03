package uptime

import (
	"sort"

	"kuso/server/internal/notify"
)

// Group turns one tick's transitions into events: one per project, or a
// single cluster-wide event when StormProjects or more unmuted projects
// changed together. Muted projects always get their own per-project
// event (the dispatcher keeps those to the bell feed) and never count
// towards the cluster-wide threshold.
func Group(downs, recovered []notify.UptimeTarget, muted map[string]bool) []notify.Event {
	var out []notify.Event
	out = append(out, groupOne(downs, muted, notify.UptimeDown, notify.UptimeDownCluster)...)
	out = append(out, groupOne(recovered, muted, notify.UptimeRecovered, notify.UptimeRecoveredCluster)...)
	return out
}

func groupOne(
	targets []notify.UptimeTarget, muted map[string]bool,
	perProject func(string, []notify.UptimeTarget) notify.Event,
	cluster func([]notify.UptimeTarget) notify.Event,
) []notify.Event {
	byProject := map[string][]notify.UptimeTarget{}
	var order []string
	for _, t := range targets {
		if _, seen := byProject[t.Project]; !seen {
			order = append(order, t.Project)
		}
		byProject[t.Project] = append(byProject[t.Project], t)
	}
	sort.Strings(order)

	var unmuted []string
	for _, p := range order {
		if !muted[p] {
			unmuted = append(unmuted, p)
		}
	}
	storm := len(unmuted) >= StormProjects

	var out []notify.Event
	var stormTargets []notify.UptimeTarget
	for _, p := range order {
		if storm && !muted[p] {
			stormTargets = append(stormTargets, byProject[p]...)
			continue
		}
		out = append(out, perProject(p, byProject[p]))
	}
	if storm {
		out = append(out, cluster(stormTargets))
	}
	return out
}
