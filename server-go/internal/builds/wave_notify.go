package builds

// One notification per push wave.
//
// A monorepo push builds every service watching that repo+branch, and the
// promotion gate (promotion_group.go) releases them together. Each build
// used to post its own "Deployed" card, so one push to an 8-service repo
// posted 8 near-identical cards a few seconds apart. Members of a wave now
// post nothing on success; the member that finishes last posts one card
// listing every service. Failures still post their own card at once: the
// wave stays held until someone retries or cancels, which can take hours.
//
// "Last" is decided in memory. The poller runs only on the leader and
// stamps every member's terminal state itself, but reads siblings from
// the informer cache, which can lag its own writes by a moment. Each
// stamp is recorded under waveMu, so whichever member records last sees
// the whole wave finished, and the claim makes the card post once.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"kuso/server/internal/kube"
)

// waveMemberResult is what the summary card shows for one service.
type waveMemberResult struct {
	Phase       string // succeeded | failed | release-failed | cancelled | superseded
	Label       string
	Envs        []string
	DurationMs  int64
	NotDeployed bool
}

type waveRecord struct {
	results map[string]waveMemberResult // by build name
	claimed bool
	touched time.Time
}

// waveRecordTTL bounds the in-memory records; a wave that has not
// finished within a day is past the promotion hold's own 2h expiry.
const waveRecordTTL = 24 * time.Hour

// waveKey identifies b's push wave, or "" when b is not a wave member
// (a manual or synthetic-ref build, or one without a repo).
func waveKey(b *kube.KusoBuild) string {
	if b == nil || b.Spec.DryRun || !shaRE.MatchString(b.Spec.Ref) || isBranchHeadBuild(b) {
		return ""
	}
	repo := buildRepoURL(b)
	if repo == "" {
		return ""
	}
	return strings.Join([]string{b.Spec.Project, repo, b.Spec.Ref, b.Spec.Branch, previewScope(b)}, "|")
}

// waveMembers returns the newest build of each service in b's wave,
// b included, sorted by service. all is the project's build list.
func waveMembers(b *kube.KusoBuild, all []kube.KusoBuild) []*kube.KusoBuild {
	key := waveKey(b)
	if key == "" {
		return nil
	}
	bySvc := map[string]*kube.KusoBuild{b.Spec.Service: b}
	for i := range all {
		s := &all[i]
		if s.Name == b.Name || s.Spec.Service == b.Spec.Service || waveKey(s) != key {
			continue
		}
		if cur := bySvc[s.Spec.Service]; cur == nil || latestOf([]*kube.KusoBuild{cur, s}) == s {
			bySvc[s.Spec.Service] = s
		}
	}
	out := make([]*kube.KusoBuild, 0, len(bySvc))
	for _, m := range bySvc {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Spec.Service < out[j].Spec.Service })
	return out
}

func terminalPhase(b *kube.KusoBuild) (string, bool) {
	switch ph := buildPhase(b); ph {
	case "succeeded", "failed", "release-failed", "cancelled":
		return ph, true
	}
	if b.Spec.Done || b.Labels["kuso.sislelabs.com/build-state"] == "done" {
		return "superseded", true
	}
	return "", false
}

// recordWaveMember notes b's terminal result and reports whether b is in
// a multi-service wave (so its own success card is skipped) and, when the
// wave just finished, the members and results the summary needs. The
// summary is returned at most once per wave.
func (p *Poller) recordWaveMember(b *kube.KusoBuild, all []kube.KusoBuild, res waveMemberResult) (inWave bool, members []*kube.KusoBuild, results map[string]waveMemberResult) {
	members = waveMembers(b, all)
	if len(members) < 2 {
		return false, nil, nil
	}
	key := waveKey(b)
	now := time.Now()
	p.waveMu.Lock()
	defer p.waveMu.Unlock()
	if p.waves == nil {
		p.waves = map[string]*waveRecord{}
	}
	for k, r := range p.waves {
		if now.Sub(r.touched) > waveRecordTTL {
			delete(p.waves, k)
		}
	}
	rec := p.waves[key]
	if rec == nil {
		rec = &waveRecord{results: map[string]waveMemberResult{}}
		p.waves[key] = rec
	}
	rec.touched = now
	rec.results[b.Name] = res
	if rec.claimed {
		return true, nil, nil
	}
	for _, m := range members {
		if _, ok := rec.results[m.Name]; ok {
			continue
		}
		if _, done := terminalPhase(m); !done {
			return true, nil, nil
		}
	}
	rec.claimed = true
	results = make(map[string]waveMemberResult, len(rec.results))
	for k, v := range rec.results {
		results[k] = v
	}
	return true, members, results
}

// waveMemberLine is one service's row: "✓ render → production · 2m 6s".
func waveMemberLine(r waveMemberResult) string {
	glyph, tail := "✓", ""
	switch r.Phase {
	case "succeeded":
		if r.NotDeployed {
			tail = "built, not deployed"
		}
	case "failed", "release-failed":
		glyph, tail = "✗", "failed"
		if r.Phase == "release-failed" {
			tail = "release failed"
		}
	case "cancelled":
		glyph, tail = "⊘", "cancelled"
	default:
		glyph, tail = "⊘", "not promoted"
	}
	line := glyph + " " + r.Label
	if len(r.Envs) > 0 && r.Phase == "succeeded" && !r.NotDeployed {
		line += " → " + strings.Join(r.Envs, ", ")
	}
	if tail != "" {
		line += " · " + tail
	}
	if r.DurationMs > 0 {
		line += " · " + formatBuildDuration(r.DurationMs)
	}
	return line
}

// waveSummaryCard renders the one card for a finished wave. b is the
// member that finished last; its commit facts stand for the whole push.
func waveSummaryCard(b *kube.KusoBuild, members []*kube.KusoBuild, results map[string]waveMemberResult, fallback func(*kube.KusoBuild) waveMemberResult) EventEnvelope {
	lines := make([]string, 0, len(members))
	ok := 0
	for _, m := range members {
		r, seen := results[m.Name]
		if !seen {
			r = fallback(m)
		}
		if r.Phase == "succeeded" && !r.NotDeployed {
			ok++
		}
		lines = append(lines, waveMemberLine(r))
	}
	title := fmt.Sprintf("✓ Deployed · %s · %d services", b.Spec.Project, len(members))
	severity := "info"
	if ok < len(members) {
		title = fmt.Sprintf("⚠ Deployed %d of %d · %s", ok, len(members), b.Spec.Project)
		severity = "warn"
	}
	// The commit facts (ref, author, trigger) are the same for every
	// member; take them off the last finisher's card.
	_, desc, facts := buildRichCard(b, "", "succeeded", "", nil)
	fields := []EnvelopeField{{Name: "Services", Value: clipField(strings.Join(lines, "\n"))}}
	for _, f := range facts {
		if f.Name == "Ref" || f.Name == "By" || f.Name == "Trigger" {
			fields = append(fields, f)
		}
	}
	return EventEnvelope{
		Type:        eventBuildSucceeded,
		Title:       title,
		Description: desc,
		Project:     b.Spec.Project,
		URL:         "/projects/" + b.Spec.Project,
		Severity:    severity,
		Fields:      fields,
	}
}

// clipField keeps a card field under Discord's 1024-character limit.
func clipField(s string) string {
	const max = 1000
	if len(s) <= max {
		return s
	}
	cut := strings.LastIndexByte(s[:max], '\n')
	if cut < 0 {
		cut = max
	}
	return s[:cut] + "\n…"
}

// noteWaveMember records b's terminal result and, when b finished its
// wave, posts the summary. It reports whether b is in a multi-service
// wave. List errors fall back to "not in a wave" so a card still posts.
func (p *Poller) noteWaveMember(ctx context.Context, ns string, b *kube.KusoBuild, res waveMemberResult) bool {
	if waveKey(b) == "" || p.Svc == nil || p.Svc.Kube == nil {
		return false
	}
	all, err := p.Svc.Kube.ListKusoBuildsByLabels(ctx, ns, map[string]string{kube.LabelProject: b.Spec.Project})
	if err != nil {
		p.logger().Warn("wave notify: list project builds", "build", b.Name, "err", err)
		return false
	}
	inWave, members, results := p.recordWaveMember(b, all, res)
	if members != nil && p.Notifier != nil {
		p.Notifier.Emit(waveSummaryCard(b, members, results, func(m *kube.KusoBuild) waveMemberResult {
			return p.waveResultFor(ctx, ns, m, "", false)
		}))
	}
	return inWave
}

// waveResultFor builds a member's row data. phase "" reads it off m.
func (p *Poller) waveResultFor(ctx context.Context, ns string, m *kube.KusoBuild, phase string, notDeployed bool) waveMemberResult {
	if phase == "" {
		phase, _ = terminalPhase(m)
		notDeployed = m.Annotations[annNotDeployed] == "true"
	}
	short := strings.TrimPrefix(m.Spec.Service, m.Spec.Project+"-")
	var kc *kube.Client
	var homeNS string
	if p.Svc != nil {
		kc, homeNS = p.Svc.Kube, p.Svc.Namespace
	}
	var envs []string
	for _, t := range lookupBuildTargets(ctx, kc, ns, homeNS, m) {
		envs = append(envs, t.Env)
	}
	return waveMemberResult{
		Phase:       phase,
		Label:       serviceDisplayLabel(ctx, kc, ns, m.Spec.Service, short),
		Envs:        envs,
		DurationMs:  buildDurationMs(m),
		NotDeployed: notDeployed,
	}
}
