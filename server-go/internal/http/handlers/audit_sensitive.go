package handlers

import (
	"context"
	"sort"

	"kuso/server/internal/audit"
)

// auditAccess records an access-control or alerting change (group grants,
// API tokens, invites, alert channels, instance settings). Severity warn
// or critical puts the row under the security retention window.
func auditAccess(ctx context.Context, a *audit.Service, sev, action, resource, msg string) {
	a.Log(ctx, audit.Entry{
		User:     auditUser(ctx),
		Severity: sev,
		Action:   action,
		Resource: resource,
		Message:  msg,
	})
}

// settingKeys lists which settings an update touched, never their values
// (some are credentials).
func settingKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
