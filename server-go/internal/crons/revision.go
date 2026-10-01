package crons

import (
	"context"
	"encoding/json"
	"strings"

	"kuso/server/internal/kube"
)

// cronRevision is the stored snapshot for cron revisions. Always
// informational: the revisions handler has no cron reverter, and command,
// url and the failure webhook can carry credentials, so only field names and
// non-sensitive scalars are kept.
type cronRevision struct {
	Op            string   `json:"op"`
	Informational bool     `json:"informational"`
	Fields        []string `json:"fields,omitempty"`
	Kind          string   `json:"kind,omitempty"`
	Schedule      string   `json:"schedule,omitempty"`
	Suspend       bool     `json:"suspend,omitempty"`
}

// recordRevision files a cron revision under the CR name minus the project
// prefix ("web-nightly" for a service cron, "report" for a project cron).
func (s *Service) recordRevision(ctx context.Context, project, fqn, op, summary string, fields []string, cr *kube.KusoCron) {
	if s.RecordRevision == nil || summary == "" {
		return
	}
	snap := cronRevision{Op: op, Informational: true, Fields: fields}
	if cr != nil {
		snap.Kind = cr.Spec.Kind
		snap.Schedule = cr.Spec.Schedule
		snap.Suspend = cr.Spec.Suspend
	}
	b, err := json.Marshal(snap)
	if err != nil {
		return
	}
	s.RecordRevision(ctx, project, "cron", strings.TrimPrefix(fqn, project+"-"), summary, b)
}

func updateSummary(fields []string) string {
	if len(fields) == 0 {
		return ""
	}
	return "cron update: " + strings.Join(fields, ", ")
}

func (r UpdateCronRequest) changedFields() []string {
	var f []string
	add := func(set bool, name string) {
		if set {
			f = append(f, name)
		}
	}
	add(r.DisplayName != nil, "displayName")
	add(r.Schedule != nil, "schedule")
	add(r.Command != nil, "command")
	add(r.Suspend != nil, "suspend")
	add(r.PinImage != nil, "pinImage")
	add(r.ConcurrencyPolicy != nil, "concurrencyPolicy")
	add(r.ActiveDeadlineSeconds != nil, "activeDeadlineSeconds")
	add(r.StartingDeadlineSeconds != nil, "startingDeadlineSeconds")
	return f
}

func (r UpdateProjectCronRequest) changedFields() []string {
	var f []string
	add := func(set bool, name string) {
		if set {
			f = append(f, name)
		}
	}
	add(r.Schedule != nil, "schedule")
	add(r.DisplayName != nil, "displayName")
	add(r.Suspend != nil, "suspend")
	add(r.PinImage != nil, "pinImage")
	add(r.URL != nil, "url")
	add(r.Image != nil, "image")
	add(r.Command != nil, "command")
	add(r.ConcurrencyPolicy != nil, "concurrencyPolicy")
	add(r.ActiveDeadlineSeconds != nil, "activeDeadlineSeconds")
	add(r.StartingDeadlineSeconds != nil, "startingDeadlineSeconds")
	add(r.OnFailure != nil, "onFailure")
	return f
}
