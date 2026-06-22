// Notification-card composition for build.* events.
// Extracted from builds.go in the v0.12 refactor pass — the builds
// package owns the rich-card field block so it doesn't need to import
// the notify package (which would create a layering inversion since
// notify itself fans-out builds events to webhooks). The wire types
// EventEnvelope + EnvelopeField mirror notify.Event + notify.EventField
// 1:1; the notify adapter passes them through.
package builds

import (
	"context"
	"fmt"
	"strings"
	"time"

	"kuso/server/internal/failures"
	"kuso/server/internal/kube"
)

// EventEnvelope is the minimum payload a notify dispatcher needs.
// Mirrors notify.Event's interesting fields without the import.
type EventEnvelope struct {
	Type     string
	Title    string
	Body     string
	Project  string
	Service  string
	URL      string
	Severity string
	Extra    map[string]string

	// Rich-card fields — same semantics as notify.Event. Builds
	// populates these when it knows the data (commit message,
	// duration, archived log tail on failure); the adapter forwards
	// 1:1, and the notify Discord renderer drops missing ones.
	Description string
	LogTail     string
	DurationMs  int64
	Fields      []EnvelopeField
	Footer      string

	// Classification, when non-nil, carries the failure kind + a
	// deep-link tab hint so the bell-popover row in the web UI can
	// route the user straight into the right tab of the service
	// overlay with the failing line highlighted. Only populated for
	// build.failed events; succeeded / cancelled / superseded leave
	// it nil and the UI falls back to the existing "open the service
	// page" behavior. See internal/failures for the kind taxonomy.
	Classification *failures.Classification
}

// EnvelopeField mirrors notify.EventField for the same import-boundary
// reason. Pure data; no methods.
type EnvelopeField struct {
	Name   string
	Value  string
	Inline bool
}

// Event type strings used at the Emit sites in this package. Kept
// here (rather than imported from notify) to preserve the
// no-notify-import boundary; the value strings must stay in sync
// with notify.Event* — covered by the notify package's
// AllEventTypes table.
const (
	eventBuildCancelled  = "build.cancelled"
	eventBuildSuperseded = "build.superseded"
	eventBuildSucceeded  = "build.succeeded"
	eventBuildFailed     = "build.failed"
)

// EventEmitter is the (notify.Dispatcher.Emit) signature the poller
// calls when a build transitions. Kept as an interface here so the
// builds package doesn't pull in notify (avoids an import cycle if
// notify ever wants build types). Nil emitter = silent.
type EventEmitter interface {
	Emit(e EventEnvelope)
}

// buildEventURL composes the dashboard path that build.* events
// link to. Empty when project or service is missing — the popover
// renders a non-clickable row in that case.
func buildEventURL(project, service string) string {
	if project == "" || service == "" {
		return ""
	}
	return fmt.Sprintf("/projects/%s?service=%s", project, service)
}

// buildRichCard assembles the title, description, and inline field
// block for a build.* notification. phase is "succeeded"/"failed";
// failureReason is the markFailed message (ignored on success).
// Description is the commit message (first line) when available;
// the field block surfaces ref + author + duration so consumers
// don't need to click through to get the basics.
//
// siteURL is the optional public URL of the deployed service ("" to
// omit). When provided it becomes a "Site" field in the card so the
// user can click straight through to the live deployment from
// Discord. Callers fetch it from the KusoService CR's first
// configured domain.
//
// Returned fields are []EnvelopeField — the notify adapter forwards
// them straight through to the Discord renderer's field block.
// label is the service name shown in the card title — the service's
// cosmetic displayName when set, else the URL slug (callers resolve it
// via serviceDisplayLabel). It's display-only; deep-link URLs + the
// envelope's Service field still use the slug.
func buildRichCard(b *kube.KusoBuild, label, phase, failureReason, siteURL string) (title, description string, fields []EnvelopeField) {
	var glyph, verb string
	switch phase {
	case "failed":
		glyph, verb = "✗", "Build failed"
	case "cancelled":
		glyph, verb = "⊘", "Build cancelled"
	case "superseded":
		glyph, verb = "⊘", "Build superseded"
	default:
		glyph, verb = "✓", "Build succeeded"
	}
	title = fmt.Sprintf("%s %s · %s / %s", glyph, verb, b.Spec.Project, label)

	annos := b.Annotations
	// Detect a synthetic ref ("<branch>-<base36-unix-ms>") produced
	// by the redeploy path when no real SHA was supplied. We do NOT
	// surface the synthetic suffix in the card — it's an internal
	// dedup token, not a meaningful git pointer.
	rawRef := b.Spec.Ref
	isSynth := b.Spec.Branch != "" &&
		strings.HasPrefix(rawRef, b.Spec.Branch+"-") &&
		!isHexSHA(rawRef)

	// Description = the human-readable commit message. Trim to the
	// first line so the card doesn't drown in a long multi-line body.
	if cm := strings.TrimSpace(annos[annCommitMessage]); cm != "" {
		if nl := strings.IndexByte(cm, '\n'); nl >= 0 {
			cm = cm[:nl]
		}
		description = cm
	} else if isSynth {
		who := strings.TrimSpace(annos[annTriggerUser])
		if who != "" {
			description = fmt.Sprintf("Manual redeploy of `%s` by %s", b.Spec.Branch, who)
		} else {
			description = fmt.Sprintf("Manual redeploy of `%s`", b.Spec.Branch)
		}
	} else if phase == "failed" && failureReason != "" {
		description = failureReason
	}

	// Field block — kept compact. Branch and ref share a row because
	// they're conceptually one pointer ("main · abcdef"); author and
	// duration each get their own slot.
	ref := rawRef
	if !isSynth && len(ref) > 7 {
		ref = ref[:7]
	}
	branchAndRef := ""
	switch {
	case isSynth && b.Spec.Branch != "":
		branchAndRef = fmt.Sprintf("`%s`", b.Spec.Branch)
	case b.Spec.Branch != "" && ref != "":
		branchAndRef = fmt.Sprintf("`%s` · `%s`", b.Spec.Branch, ref)
	case b.Spec.Branch != "":
		branchAndRef = fmt.Sprintf("`%s`", b.Spec.Branch)
	case ref != "":
		branchAndRef = fmt.Sprintf("`%s`", ref)
	}
	if branchAndRef != "" {
		fields = append(fields, EnvelopeField{Name: "Ref", Value: branchAndRef, Inline: true})
	}
	if user := strings.TrimSpace(annos[annTriggerUser]); user != "" {
		fields = append(fields, EnvelopeField{Name: "By", Value: user, Inline: true})
	} else if src := strings.TrimSpace(annos[annTriggerSource]); src != "" {
		fields = append(fields, EnvelopeField{Name: "By", Value: src, Inline: true})
	}
	if d := buildDurationMs(b); d > 0 {
		var label string
		switch phase {
		case "failed":
			label = "Failed after"
		case "cancelled", "superseded":
			label = "Stopped after"
		default:
			label = "Built in"
		}
		fields = append(fields, EnvelopeField{
			Name:   label,
			Value:  formatBuildDuration(d),
			Inline: true,
		})
	}
	// "Site" field links to the live deployment. Only shown for
	// succeeded builds since failed/cancelled/superseded builds don't
	// produce a new live URL anyway.
	if phase == "succeeded" && siteURL != "" {
		fields = append(fields, EnvelopeField{
			Name:   "Site",
			Value:  fmt.Sprintf("[%s](%s)", siteHostFromURL(siteURL), siteURL),
			Inline: true,
		})
	}
	return title, description, fields
}

// siteHostFromURL strips https:// (or http://) and any trailing slash
// from a URL so the Discord card shows "web.distill.sislelabs.com"
// instead of the full URL — the markdown link target carries the
// scheme so the click still works.
func siteHostFromURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimSuffix(u, "/")
	return u
}

// buildDurationMs reads start + completed timestamps off the build CR
// and returns the wall-clock duration in ms. Returns 0 when either
// stamp is missing (e.g. a build that failed before the pod ever
// started) so the renderer drops the field gracefully.
func buildDurationMs(b *kube.KusoBuild) int64 {
	if b == nil {
		return 0
	}
	start := strings.TrimSpace(b.Annotations[annStartedAt])
	end := strings.TrimSpace(b.Annotations[annCompletedAt])
	if start == "" || end == "" {
		return 0
	}
	startT, err := time.Parse(time.RFC3339, start)
	if err != nil {
		return 0
	}
	endT, err := time.Parse(time.RFC3339, end)
	if err != nil {
		return 0
	}
	d := endT.Sub(startT)
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// lookupSiteURL resolves the public URL of a service for inclusion in
// notification cards. Returns "" when no public host can be resolved, or
// on any kube lookup error (we don't fail the notification over a missing
// site link).
//
// fqn is the service's KusoService CR name (e.g. "distill-web"), not the
// short alias. project is the owning project (for the env lookup).
//
// Resolution order:
//  1. The service's first explicit spec.domains[] entry (a user-pinned
//     custom domain), https/http per its TLS flag.
//  2. The PRODUCTION KusoEnvironment's resolved host. This is the common
//     case — most services have no explicit spec.domains and rely on the
//     auto-generated <service>.<baseDomain> host, which the server writes
//     onto the env CR's spec.host. Without this fallback the "Site" link
//     was silently dropped for every auto-host service (e.g. scaffold).
//
// Internal-only services have no public URL, so they resolve to "".
func lookupSiteURL(ctx context.Context, kc *kube.Client, ns, project, fqn string) string {
	if kc == nil || ns == "" || fqn == "" {
		return ""
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	// 1) Explicit service-level custom domain wins.
	if svc, err := kc.GetKusoService(lctx, ns, fqn); err == nil && svc != nil && len(svc.Spec.Domains) > 0 {
		if host := strings.TrimSpace(svc.Spec.Domains[0].Host); host != "" {
			scheme := "https"
			if !svc.Spec.Domains[0].TLS {
				scheme = "http"
			}
			return scheme + "://" + host
		}
	}

	// 2) Fall back to the production env's resolved host (the auto-
	//    generated <svc>.<baseDomain> lives here even when the service
	//    has no explicit spec.domains).
	short := strings.TrimPrefix(fqn, project+"-")
	envs, err := kc.ListKusoEnvironmentsByLabels(lctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: short,
	})
	if err != nil {
		return ""
	}
	for i := range envs {
		e := &envs[i]
		if e.Spec.Kind != "production" {
			continue
		}
		// Internal-only services aren't reachable from outside the
		// cluster — no public link to offer.
		if e.Spec.Internal {
			return ""
		}
		host := strings.TrimSpace(e.Spec.Host)
		if host == "" && len(e.Spec.AdditionalHosts) > 0 {
			host = strings.TrimSpace(e.Spec.AdditionalHosts[0])
		}
		if host == "" {
			return ""
		}
		scheme := "http"
		for _, th := range e.Spec.TLSHosts {
			if th == host {
				scheme = "https"
				break
			}
		}
		return scheme + "://" + host
	}
	return ""
}

// serviceDisplayLabel returns the name to show for a service in
// notification titles: the service's cosmetic spec.displayName when set,
// otherwise the URL slug (short). Best-effort — any kube error falls
// back to the slug so a notification never fails to send over a naming
// lookup. fqn is the full CR name (<project>-<service>); short is the
// already-computed slug.
func serviceDisplayLabel(ctx context.Context, kc *kube.Client, ns, fqn, short string) string {
	if kc == nil {
		return short
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if svc, err := kc.GetKusoService(lctx, ns, fqn); err == nil && svc != nil {
		if dn := strings.TrimSpace(svc.Spec.DisplayName); dn != "" {
			return dn
		}
	}
	return short
}

// isHexSHA returns true when s is a hex-only string of 7+ characters.
// Used to discriminate a real (possibly trimmed) git SHA from the
// synthetic "<branch>-<base36>" refs the redeploy path generates.
// Lowercase only — git outputs lowercase SHAs everywhere.
func isHexSHA(s string) bool {
	if len(s) < 7 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// formatBuildDuration prints a compact human duration: "12s", "1m 24s",
// "2h 5m". Caps at hours+minutes — a multi-day build would be a real
// problem we'd want surfaced differently anyway.
func formatBuildDuration(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d / time.Minute)
		s := int((d % time.Minute) / time.Second)
		if s == 0 {
			return fmt.Sprintf("%dm", m)
		}
		return fmt.Sprintf("%dm %ds", m, s)
	}
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh %dm", h, m)
}
