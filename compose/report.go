package compose

import (
	"fmt"
	"sort"
	"strings"
)

// Action classifies a single conversion decision for the report.
type Action string

const (
	// ActionService — a compose service became a kuso service.
	ActionService Action = "service"
	// ActionAddon — a compose service was detected as a datastore and
	// became a managed kuso addon.
	ActionAddon Action = "addon"
	// ActionFlag — something needs the user's attention before this
	// import is deployable (e.g. a build service with no repo).
	ActionFlag Action = "flag"
	// ActionSkip — a compose key with no kuso equivalent was not
	// imported. Recorded so nothing is silently dropped.
	ActionSkip Action = "skip"
)

// Note is one line of the conversion report.
type Note struct {
	Action  Action `json:"action"`
	Service string `json:"service"` // compose service this concerns ("" = file-level)
	Detail  string `json:"detail"`
}

// Report accumulates every decision the converter made. It is part of
// the API surface (the web preview renders it) so the fields are
// JSON-tagged and stable.
type Report struct {
	Notes []Note `json:"notes"`
}

func (r *Report) add(a Action, service, format string, args ...any) {
	r.Notes = append(r.Notes, Note{Action: a, Service: service, Detail: fmt.Sprintf(format, args...)})
}

// Service records a compose-service → kuso-service mapping.
func (r *Report) service(svc, format string, args ...any) { r.add(ActionService, svc, format, args...) }

// Addon records a compose-service → kuso-addon mapping.
func (r *Report) addon(svc, format string, args ...any) { r.add(ActionAddon, svc, format, args...) }

// Flag records something the user must resolve before deploying.
func (r *Report) flag(svc, format string, args ...any) { r.add(ActionFlag, svc, format, args...) }

// Skip records a compose key that has no kuso equivalent.
func (r *Report) skip(svc, format string, args ...any) { r.add(ActionSkip, svc, format, args...) }

// HasFlags reports whether any note needs user attention before the
// import is deployable.
func (r *Report) HasFlags() bool {
	for _, n := range r.Notes {
		if n.Action == ActionFlag {
			return true
		}
	}
	return false
}

func actionGlyph(a Action) string {
	switch a {
	case ActionService:
		return "✓ service"
	case ActionAddon:
		return "✓ addon"
	case ActionFlag:
		return "⚠ flag"
	case ActionSkip:
		return "⊘ skip"
	}
	return string(a)
}

// Markdown renders the report as a table grouped by service, suitable
// for the CLI dry-run output. File-level notes (Service == "") sort
// first under a "(file)" heading.
func (r *Report) Markdown() string {
	byService := map[string][]Note{}
	order := []string{}
	for _, n := range r.Notes {
		key := n.Service
		if key == "" {
			key = "(file)"
		}
		if _, ok := byService[key]; !ok {
			order = append(order, key)
		}
		byService[key] = append(byService[key], n)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i] == "(file)" {
			return true
		}
		if order[j] == "(file)" {
			return false
		}
		return order[i] < order[j]
	})

	var b strings.Builder
	for _, svc := range order {
		fmt.Fprintf(&b, "### %s\n\n", svc)
		fmt.Fprintf(&b, "| action | detail |\n| --- | --- |\n")
		for _, n := range byService[svc] {
			fmt.Fprintf(&b, "| %s | %s |\n", actionGlyph(n.Action), n.Detail)
		}
		fmt.Fprintln(&b)
	}
	return b.String()
}
