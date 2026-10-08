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
	"net/url"
	"sort"
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
	// Env is the env group the build targets ("" when none/several);
	// Links is the card's action row (buildCardLinks).
	Env   string
	Links []EnvelopeLink

	// Classification, when non-nil, carries the failure kind + a
	// deep-link tab hint so the bell-popover row in the web UI can
	// route the user straight into the right tab of the service
	// overlay with the failing line highlighted. Only populated for
	// build.failed events; succeeded / cancelled / superseded leave
	// it nil and the UI falls back to the existing "open the service
	// page" behavior. See internal/failures for the kind taxonomy.
	Classification *failures.Classification
}

// EnvelopeLink mirrors notify.EventLink for the same import-boundary
// reason.
type EnvelopeLink struct {
	Label string
	URL   string
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
	eventBuildCancelled   = "build.cancelled"
	eventBuildSuperseded  = "build.superseded"
	eventBuildSucceeded   = "build.succeeded"
	eventBuildFailed      = "build.failed"
	eventDeployRolledBack = "deploy.rolledback"
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

// withEnvParam pins the project page's env selector (?env=<group>) when
// the build targets exactly one env, so the click opens that env rather
// than whichever one the page defaults to (production).
func withEnvParam(u string, targets []buildTarget) string {
	if u == "" || len(targets) != 1 || targets[0].Env == "" {
		return u
	}
	return u + "&env=" + url.QueryEscape(targets[0].Env)
}

// buildRichCard assembles the title, description, and inline field
// block for a build.* notification. phase is succeeded / failed /
// cancelled / superseded; failureReason is why a non-succeeded build
// stopped (ignored on success).
// Description is the commit message (first line) when available;
// the field block surfaces ref + author + duration so consumers
// don't need to click through to get the basics.
//
// targets are the envs the build deploys to (lookupBuildTargets; nil
// when unknown). Their names go in the title so a staging or preview
// build isn't mistaken for a production deploy; their public URLs go
// in the link row (buildCardLinks), not the field block.
//
// Returned fields are []EnvelopeField — the notify adapter forwards
// them straight through to the Discord renderer's field block.
// label is the service name shown in the card title — the service's
// cosmetic displayName when set, else the URL slug (callers resolve it
// via serviceDisplayLabel). It's display-only; deep-link URLs + the
// envelope's Service field still use the slug.
func buildRichCard(b *kube.KusoBuild, label, phase, failureReason string, targets []buildTarget) (title, description string, fields []EnvelopeField) {
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
	if len(targets) > 0 {
		envs := make([]string, len(targets))
		for i, t := range targets {
			envs[i] = t.Env
		}
		title += " → " + strings.Join(envs, ", ")
	}

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
	} else if failureReason != "" && phase != "succeeded" {
		description = failureReason
	}

	// Field block — kept compact. Branch and ref share a row because
	// they're conceptually one pointer ("main · abcdef"); author and
	// duration each get their own slot. Both link to the forge when the
	// repo URL is a recognised host.
	var repoURL string
	if b.Spec.Repo != nil {
		repoURL = b.Spec.Repo.URL
	}
	commitURL := ""
	if !isSynth && isHexSHA(rawRef) {
		commitURL = forgeCommitURL(repoURL, rawRef)
	}
	ref := rawRef
	if !isSynth && len(ref) > 7 {
		ref = ref[:7]
	}
	branchMD := ""
	if b.Spec.Branch != "" {
		branchMD = mdCodeLink(b.Spec.Branch, forgeBranchURL(repoURL, b.Spec.Branch))
	}
	branchAndRef := ""
	switch {
	case isSynth && branchMD != "":
		branchAndRef = branchMD
	case branchMD != "" && ref != "":
		branchAndRef = branchMD + " · " + mdCodeLink(ref, commitURL)
	case branchMD != "":
		branchAndRef = branchMD
	case ref != "":
		branchAndRef = mdCodeLink(ref, commitURL)
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
	// The commit message took the description slot, so the reason
	// would otherwise only live in Body — which no renderer shows once a
	// Description is set. Full-width so a long kaniko error stays legible.
	if failureReason != "" && phase != "succeeded" && description != failureReason {
		fields = append(fields, EnvelopeField{Name: "Reason", Value: failureReason})
	}
	return title, description, fields
}

// mdCodeLink renders text as inline code, wrapped in a markdown link
// when href is set.
func mdCodeLink(text, href string) string {
	if href == "" {
		return "`" + text + "`"
	}
	return "[`" + text + "`](" + href + ")"
}

// forgeFlavor is the URL layout a repo host uses for commit + branch pages.
type forgeFlavor int

const (
	forgeNone forgeFlavor = iota
	forgeGitHub
	forgeGitLab
	forgeGitea
)

// forgeRepo normalises a clone URL (https or git@host:o/r, optional
// ".git") into its https web base and the host's URL layout. forgeNone
// for hosts we can't vouch for — a guessed link that 404s is worse than
// plain text.
func forgeRepo(raw string) (base string, flavor forgeFlavor) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", forgeNone
	}
	var host, path string
	if rest, ok := strings.CutPrefix(raw, "git@"); ok {
		h, p, found := strings.Cut(rest, ":")
		if !found {
			return "", forgeNone
		}
		host, path = h, p
	} else {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
			return "", forgeNone
		}
		host, path = u.Host, u.Path
	}
	host = strings.ToLower(host)
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	if path == "" || !strings.Contains(path, "/") {
		return "", forgeNone
	}
	switch {
	case host == "github.com":
		flavor = forgeGitHub
	case host == "gitlab.com" || strings.HasPrefix(host, "gitlab."):
		flavor = forgeGitLab
	case host == "codeberg.org" || strings.HasPrefix(host, "gitea."):
		flavor = forgeGitea
	default:
		return "", forgeNone
	}
	return "https://" + host + "/" + path, flavor
}

// forgeCommitURL links a commit SHA on the repo's forge; "" when the
// host is unrecognised.
func forgeCommitURL(repoURL, sha string) string {
	base, flavor := forgeRepo(repoURL)
	switch flavor {
	case forgeGitHub, forgeGitea:
		return base + "/commit/" + url.PathEscape(sha)
	case forgeGitLab:
		return base + "/-/commit/" + url.PathEscape(sha)
	}
	return ""
}

// forgeBranchURL links a branch's tree on the repo's forge. Each path
// segment is escaped but the slashes are kept, since forges route
// "feat/x" as a nested path.
func forgeBranchURL(repoURL, branch string) string {
	base, flavor := forgeRepo(repoURL)
	if flavor == forgeNone || branch == "" {
		return ""
	}
	segs := strings.Split(branch, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	escaped := strings.Join(segs, "/")
	switch flavor {
	case forgeGitHub:
		return base + "/tree/" + escaped
	case forgeGitLab:
		return base + "/-/tree/" + escaped
	case forgeGitea:
		return base + "/src/branch/" + escaped
	}
	return ""
}

// buildSeverity is notify.EnvSeverity applied to a build's targets: a
// failure is error (default @here) only when it can affect production —
// a production target, or targets we couldn't resolve. Everything else
// is capped at warn so a broken preview doesn't page the channel.
func buildSeverity(targets []buildTarget) string {
	if len(targets) == 0 {
		return "error"
	}
	for _, t := range targets {
		if t.Env == "" || t.Env == "production" {
			return "error"
		}
	}
	return "warn"
}

// singleTargetEnv is the envelope's Env: set only when the build lands
// in exactly one env.
func singleTargetEnv(targets []buildTarget) string {
	if len(targets) == 1 {
		return targets[0].Env
	}
	return ""
}

// buildCardLinks is the action row for a build.* card. phase is
// succeeded / failed / release-failed / cancelled / superseded. c is
// the failure classification (failed only; may be nil).
func buildCardLinks(project, short, phase string, targets []buildTarget, c *failures.Classification) []EnvelopeLink {
	base := buildEventURL(project, short)
	if base == "" {
		return nil
	}
	deployments := withEnvParam(base+"&tab=deployments", targets)
	var links []EnvelopeLink
	switch phase {
	case "failed":
		logs := base + "&tab=deployments"
		if c != nil && c.Kind != "" {
			logs += "&kind=" + url.QueryEscape(string(c.Kind))
		}
		// One link: the web has no per-build deep link, so a separate
		// "Deployments" would open the same tab minus the failure banner.
		links = append(links, EnvelopeLink{Label: "View failure", URL: withEnvParam(logs, targets)})
	case "release-failed":
		links = append(links, EnvelopeLink{Label: "Deployments", URL: deployments})
	case "succeeded":
		links = append(links, EnvelopeLink{Label: "Deployments", URL: deployments})
		for _, t := range targets {
			if t.URL != "" {
				links = append(links, EnvelopeLink{Label: "Open " + t.Env, URL: t.URL})
			}
		}
	default:
		links = append(links, EnvelopeLink{Label: "Deployments", URL: deployments})
	}
	return links
}

// replacedByDescription is the superseded-card fallback description
// (used when the build has no commit message). newerRef is the
// replacing build's ref when known; synthetic refs are dropped.
func replacedByDescription(branch, newerRef string) string {
	if isHexSHA(newerRef) {
		if len(newerRef) > 7 {
			newerRef = newerRef[:7]
		}
		return "Replaced by a newer build (`" + newerRef + "`)"
	}
	if branch != "" {
		return "Replaced by a newer build of `" + branch + "`"
	}
	return "Replaced by a newer build"
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

// buildTarget is one environment a build promotes into: its env-group
// name (production / staging / preview-pr-7 / custom) and the public URL
// it serves ("" for internal-only envs or when no host resolves).
type buildTarget struct {
	Env string
	URL string
}

// lookupBuildTargets resolves the environments a build deploys to, using
// the same branch rule as promoteImage (promotionBranchMatches), so the
// card names the env the build actually lands in and links THAT env's
// host. Before this every card linked the production host, even for
// staging / preview / custom-env builds.
//
// fqn-level spec.domains[] are custom domains pinned on the production
// env, so they only override the production target's URL.
//
// homeNS is where the KusoProject lives (for the default branch); ns is
// the execution namespace holding the service + env CRs. Best-effort:
// any kube error yields fewer (or no) targets, never a failed notify.
func lookupBuildTargets(ctx context.Context, kc *kube.Client, ns, homeNS string, b *kube.KusoBuild) []buildTarget {
	if kc == nil || ns == "" || b == nil || b.Spec.Service == "" || b.Spec.DryRun {
		return nil
	}
	lctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	project, fqn := b.Spec.Project, b.Spec.Service
	short := strings.TrimPrefix(fqn, project+"-")
	envs, err := kc.ListKusoEnvironmentsByLabels(lctx, ns, map[string]string{
		kube.LabelProject: project,
		kube.LabelService: short,
	})
	if err != nil || len(envs) == 0 {
		return nil
	}
	pp, perr := kc.GetKusoProject(lctx, homeNS, project)
	if perr != nil {
		pp = nil
	}
	svc, serr := kc.GetKusoService(lctx, ns, fqn)
	if serr != nil {
		svc = nil
	}
	defaultBranch := effectiveDefaultBranch(pp, svc)
	var domainURL string
	if svc != nil && len(svc.Spec.Domains) > 0 {
		if host := strings.TrimSpace(svc.Spec.Domains[0].Host); host != "" {
			scheme := "https"
			if !svc.Spec.Domains[0].TLS {
				scheme = "http"
			}
			domainURL = scheme + "://" + host
		}
	}

	var out []buildTarget
	for i := range envs {
		e := &envs[i]
		if e.Spec.Service != "" && e.Spec.Service != fqn {
			continue
		}
		if !promotionBranchMatches(b.Spec.Branch, e.Spec.Branch, defaultBranch) {
			continue
		}
		// Env-GROUP label, not spec.kind — clones set kind=production
		// but belong to their own group.
		name := e.Labels[kube.LabelEnv]
		if name == "" {
			name = strings.TrimPrefix(e.Name, fqn+"-")
		}
		t := buildTarget{Env: name}
		switch {
		case e.Spec.Internal:
		case name == "production" && domainURL != "":
			t.URL = domainURL
		default:
			t.URL = envPublicURL(e)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Env == "production") != (out[j].Env == "production") {
			return out[i].Env == "production"
		}
		return out[i].Env < out[j].Env
	})
	return out
}

// envPublicURL returns the env's primary public URL, https when the host
// is TLS-eligible. "" when the env has no host.
func envPublicURL(e *kube.KusoEnvironment) string {
	host := strings.TrimSpace(e.Spec.Host)
	if host == "" && len(e.Spec.AdditionalHosts) > 0 {
		host = strings.TrimSpace(e.Spec.AdditionalHosts[0])
	}
	if host == "" {
		return ""
	}
	for _, th := range e.Spec.TLSHosts {
		if th == host {
			return "https://" + host
		}
	}
	return "http://" + host
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
