package handlers

import (
	"context"

	"kuso/server/internal/db"
)

// LiveScopes is the set of projects and services that exist right now:
// project → service short name → true.
type LiveScopes map[string]map[string]bool

// keeps reports whether an event still points at something that exists.
// Events with no project (node, platform, update notices) always stay; an
// event naming a service stays only while that service does. Addon events
// carry no service, so they live and die with their project.
func (l LiveScopes) keeps(e db.NotificationEvent) bool {
	if e.Project == "" {
		return true
	}
	svcs, ok := l[e.Project]
	if !ok {
		return false
	}
	return e.Service == "" || svcs[e.Service]
}

// dropGhostEvents hides events for deleted projects and services, so the
// feed and the unread badge stop showing "Crashing · demo/stucktest" for a
// service nobody can open any more. A nil live set (no kube wiring, or the
// lookup failed) keeps everything.
func dropGhostEvents(events []db.NotificationEvent, live LiveScopes) []db.NotificationEvent {
	if live == nil {
		return events
	}
	out := events[:0]
	for _, e := range events {
		if live.keeps(e) {
			out = append(out, e)
		}
	}
	return out
}

// liveScopes resolves the live set, logging (not failing) on error.
func (h *NotificationsHandler) liveScopes(ctx context.Context) LiveScopes {
	if h.Live == nil {
		return nil
	}
	l, err := h.Live(ctx)
	if err != nil {
		if h.Logger != nil {
			h.Logger.Warn("notifications: live scope lookup failed; not filtering deleted services", "err", err)
		}
		return nil
	}
	return l
}
