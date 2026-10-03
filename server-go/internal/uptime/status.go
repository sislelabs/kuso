package uptime

import (
	"sort"
	"time"

	"kuso/server/internal/db"
)

// ServiceStatus is one service's entry in GET /api/projects/{p}/uptime.
type ServiceStatus struct {
	Service string `json:"service"`
	Env     string `json:"env"`
	// State: up | failing | down | paused | disabled | pending.
	State string `json:"state"`
	// Reason: why it's failing-but-not-alerted, or why it's paused.
	Reason        string     `json:"reason,omitempty"`
	Since         *time.Time `json:"since,omitempty"`
	LastCheckedAt *time.Time `json:"lastCheckedAt,omitempty"`
	LatencyMs     int        `json:"latencyMs,omitempty"`
	StatusCode    int        `json:"statusCode,omitempty"`
	Error         string     `json:"error,omitempty"`
	URL           string     `json:"url"`
}

// Status joins a project's targets with their stored rows.
func Status(project string, targets []Target, rows []db.UptimeState) []ServiceStatus {
	byKey := make(map[string]db.UptimeState, len(rows))
	for _, r := range rows {
		byKey[r.Namespace+"/"+r.Env] = r
	}
	out := []ServiceStatus{}
	for _, t := range targets {
		if t.Project != project {
			continue
		}
		s := ServiceStatus{Service: t.Service, Env: t.Env, URL: t.URL}
		row, has := byKey[t.Key()]
		switch {
		case t.Disabled:
			s.State = "disabled"
		case t.Paused != "":
			s.State, s.Reason = "paused", t.Paused
		case !has || row.LastCheckedAt.IsZero():
			s.State = "pending"
		default:
			checked := row.LastCheckedAt
			s.LastCheckedAt = &checked
			s.LatencyMs, s.StatusCode, s.Error = row.LastLatencyMs, row.LastStatusCode, row.LastError
			switch {
			case row.Alerted:
				s.State = "down"
			case !row.DownSince.IsZero():
				s.State, s.Reason = "failing", row.Hold
			default:
				s.State = "up"
			}
			if !row.DownSince.IsZero() {
				since := row.DownSince
				s.Since = &since
			}
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Service < out[j].Service })
	return out
}
