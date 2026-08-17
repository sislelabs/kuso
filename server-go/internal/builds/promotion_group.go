package builds

// Atomic promotion for same-repo services (monorepos).
//
// A push to a monorepo triggers one build per service watching that
// repo+branch — all carrying the same commit SHA. Promotion used to be
// per-build: if the CMS build succeeded and the internal-system build
// failed, the CMS rolled to the new commit while internal-system kept
// running the old one — a version mismatch between two halves of the
// same codebase (the scubatony dc6ed19 incident).
//
// The gate: before promoting a webhook (real-SHA) build, look at every
// OTHER service in the project that builds from the same repo and has
// a build for the same commit. Until every one of them is green, the
// promotion is HELD — the build stays non-terminal (annotated with the
// hold reason) and the poller re-checks each tick. When the failed
// sibling is retried and goes green, the whole wave promotes together.
//
// Deadlock-avoidance rules, each load-bearing:
//   - A held build annotates itself (annPromoteHold). The annotation
//     doubles as the "my Job is green, I'm just waiting" signal — two
//     builds finishing near-simultaneously would otherwise each see
//     the other as non-terminal and wait forever.
//   - A FAILED sibling is forgiven once a NEWER build of that service
//     went green (retries often carry a synthetic ref, so they never
//     join the SHA wave — judging only the wave would hold forever
//     while the retry promoted alone).
//   - A NEWER build for the held service itself (next push) supersedes
//     the held one: stamped terminal-not-promoted so its own service
//     queue advances instead of deadlocking behind the hold.
//
// Escape hatches, deliberate:
//   - Manual triggers (kuso build trigger / UI redeploy) synthesize a
//     non-SHA ref → never gated. Forcing one service out alone is a
//     two-keystroke operator action, not a config knob.
//   - A CANCELLED sibling build counts as "operator said skip it" and
//     doesn't hold the wave.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// normalizePromoRepoURL canonicalizes a repo URL for group matching:
// credentials, scheme case, trailing "/" and ".git" must not split a
// group. Empty stays empty (no grouping).
func normalizePromoRepoURL(raw string) string {
	u := strings.TrimSpace(kube.StripRepoURLCredentials(raw))
	u = strings.TrimSuffix(u, "/")
	u = strings.TrimSuffix(u, ".git")
	// Lowercase the whole thing: hosts are case-insensitive and the
	// GitHub/GitLab path namespace is unique case-insensitively —
	// safer to group than to split.
	u = strings.ToLower(u)
	// Unify transport forms: https://host/org/repo, ssh://git@host/…,
	// and scp-style git@host:org/repo are the same repo. Strip scheme
	// + userinfo, flip the scp host:path colon to a slash.
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	if i := strings.IndexByte(u, '@'); i >= 0 && !strings.ContainsAny(u[:i], "/") {
		u = u[i+1:]
	}
	if slash := strings.IndexByte(u, '/'); slash > 0 {
		if colon := strings.IndexByte(u[:slash], ':'); colon > 0 {
			u = u[:colon] + "/" + u[colon+1:]
		}
	} else if colon := strings.IndexByte(u, ':'); colon > 0 {
		u = u[:colon] + "/" + u[colon+1:]
	}
	return u
}

func buildRepoURL(b *kube.KusoBuild) string {
	if b == nil || b.Spec.Repo == nil {
		return ""
	}
	return normalizePromoRepoURL(b.Spec.Repo.URL)
}

// latestOf returns the newest build in list (creation time, name as
// tie-break), or nil for an empty list.
func latestOf(list []*kube.KusoBuild) *kube.KusoBuild {
	var out *kube.KusoBuild
	for _, s := range list {
		if out == nil || s.CreationTimestamp.After(out.CreationTimestamp.Time) ||
			(s.CreationTimestamp.Equal(&out.CreationTimestamp) && s.Name > out.Name) {
			out = s
		}
	}
	return out
}

// promotionHoldVerdict decides whether build b must wait before
// promoting. all is every live KusoBuild CR in b's project (one
// label-list). Returns "" to proceed, else a human-readable hold
// reason. Pure — no kube access — so the decision matrix is
// unit-testable.
func promotionHoldVerdict(b *kube.KusoBuild, all []kube.KusoBuild) string {
	if b == nil || !shaRE.MatchString(b.Spec.Ref) {
		return "" // manual/synthetic-ref build — never gated
	}
	repo := buildRepoURL(b)
	if repo == "" {
		return ""
	}
	// Per sibling service: its builds for THIS commit+repo (the wave)
	// and all its SAME-BRANCH builds (for the forgiveness rule).
	//
	// Branch scoping is load-bearing everywhere here: preview builds
	// carry the real PR-head SHA on the same service/labels, and
	// staging-tracked siblings live on other branches. Without the
	// branch filter, a failed PREVIEW build of a fast-forwarded SHA
	// joins the production wave (permanent hold), and a green preview
	// build "forgives" a failed production sibling (atomicity broken).
	waveBySvc := map[string][]*kube.KusoBuild{}
	branchBySvc := map[string][]*kube.KusoBuild{}
	for i := range all {
		s := &all[i]
		if s.Name == b.Name || s.Spec.Service == b.Spec.Service || s.Spec.Project != b.Spec.Project {
			continue
		}
		if s.Spec.Branch != b.Spec.Branch {
			continue
		}
		branchBySvc[s.Spec.Service] = append(branchBySvc[s.Spec.Service], s)
		if s.Spec.Ref == b.Spec.Ref && buildRepoURL(s) == repo {
			waveBySvc[s.Spec.Service] = append(waveBySvc[s.Spec.Service], s)
		}
	}
	svcs := make([]string, 0, len(waveBySvc))
	for svc := range waveBySvc {
		svcs = append(svcs, svc)
	}
	// Deterministic order so the hold reason is stable across ticks
	// (the annotation only re-patches on change).
	sort.Strings(svcs)
	for _, svc := range svcs {
		s := latestOf(waveBySvc[svc])
		short := strings.TrimPrefix(svc, b.Spec.Project+"-")
		switch buildPhase(s) {
		case "succeeded":
			continue // green
		case "cancelled":
			continue // operator explicitly stopped it — skip, don't hold
		case "failed", "release-failed":
			// Forgiveness: a retry usually carries a synthetic ref and
			// never joins this SHA's wave. If the sibling's newest
			// SAME-BRANCH build is newer than the failure, judge that.
			newest := latestOf(branchBySvc[svc])
			if newest != nil && newest.Name != s.Name &&
				newest.CreationTimestamp.After(s.CreationTimestamp.Time) {
				switch buildPhase(newest) {
				case "succeeded", "cancelled":
					continue // retry went green (or operator skipped)
				case "failed", "release-failed":
					return fmt.Sprintf("sibling build failed: %s (%s) — same-repo services promote atomically; retry it (or cancel it) to release this build", short, newest.Name)
				default:
					if newest.Annotations[annPromoteHold] != "" {
						continue // retry green, itself awaiting the wave
					}
					return fmt.Sprintf("waiting for sibling build: %s (%s) — same-repo services promote atomically", short, newest.Name)
				}
			}
			return fmt.Sprintf("sibling build failed: %s (%s) — same-repo services promote atomically; retry it (or cancel it) to release this build", short, s.Name)
		default:
			// Non-terminal. A hold annotation means its Job is green
			// and it's waiting on the wave too — counts as green, or
			// two simultaneous finishers deadlock on each other.
			if s.Annotations[annPromoteHold] != "" {
				continue
			}
			return fmt.Sprintf("waiting for sibling build: %s (%s) — same-repo services promote atomically", short, s.Name)
		}
	}
	return ""
}

// newerBuildOf returns the name of the newest SAME-BRANCH build for
// b's own service created after b, or "" when b is the newest. Any
// state counts — queued, running, done: a successor's existence means
// this held build's commit is no longer the target for its env.
//
// Same-branch only: a PREVIEW build (PR-head SHA, different branch)
// targets a preview env — letting it supersede a held PRODUCTION
// build would cancel the production rollout and strand prod on the
// old commit, the exact incident class this gate exists to prevent.
func newerBuildOf(b *kube.KusoBuild, all []kube.KusoBuild) string {
	newest := ""
	newestTS := b.CreationTimestamp
	for i := range all {
		s := &all[i]
		if s.Name == b.Name || s.Spec.Service != b.Spec.Service || s.Spec.Branch != b.Spec.Branch {
			continue
		}
		if s.CreationTimestamp.After(newestTS.Time) {
			newest, newestTS = s.Name, s.CreationTimestamp
		}
	}
	return newest
}

// notePromotionHold annotates b with the hold reason (patching only on
// change so a steady hold is one write, not one per 5s tick) and logs
// the transition.
func (p *Poller) notePromotionHold(ctx context.Context, ns string, b *kube.KusoBuild, reason string) {
	if b.Annotations[annPromoteHold] == reason {
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annPromoteHold, reason)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		p.logger().Warn("promotion hold: annotate failed", "build", b.Name, "err", err)
		return
	}
	firstHold := b.Annotations[annPromoteHold] == ""
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annPromoteHold] = reason
	p.logger().Info("promotion held (same-repo atomic gate)", "build", b.Name, "reason", reason)
	if firstHold {
		// Snapshot the build logs NOW: a hold can outlive the Job's 1h
		// TTL, after which the pods (and their logs) are gone. The
		// record upserts again at the real terminal edge.
		p.queueArchive(ctx, ns, b, "succeeded")
	}
}

// clearPromotionHold removes a stale hold annotation once the wave is
// green (or the gate no longer applies). No-op when absent.
func (p *Poller) clearPromotionHold(ctx context.Context, ns string, b *kube.KusoBuild) {
	if b.Annotations[annPromoteHold] == "" {
		return
	}
	patch := fmt.Sprintf(`{"metadata":{"annotations":{%q:null}}}`, annPromoteHold)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		p.logger().Warn("promotion hold: clear failed", "build", b.Name, "err", err)
		return
	}
	delete(b.Annotations, annPromoteHold)
	p.logger().Info("promotion hold released — promoting", "build", b.Name)
}

// stampHeldSuperseded terminates a held build whose service has a
// newer build: its commit is no longer the target, and leaving it
// active would deadlock its own service's queue behind a hold that
// can only resolve on the next wave. Mirrors the supersede shape
// admission.go uses (phase=cancelled + superseded-by) so every
// existing surface renders it correctly.
func (p *Poller) stampHeldSuperseded(ctx context.Context, ns string, b *kube.KusoBuild, newer string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(
		`{"metadata":{"annotations":{%q:"cancelled",%q:%q,%q:%q,%q:%q,%q:null},"labels":{"kuso.sislelabs.com/build-state":"done"}},"spec":{"done":true}}`,
		annPhase,
		annCompletedAt, now,
		annSupersededBy, newer,
		annMessage, "not promoted: superseded by "+newer+" while promotion was held for sibling builds",
		annPromoteHold,
	)
	if _, err := p.Svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace(ns).
		Patch(ctx, b.Name, types.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		return fmt.Errorf("stamp held build superseded: %w", err)
	}
	// Same terminal-edge hygiene as every other terminal path: the
	// per-build clone-token Secret must not outlive the build.
	p.deleteCloneTokenSecret(ns, b.Name)
	// Mirror the stamp for the record upsert (archiveRecord reads
	// b.Annotations), then update the archived record to its real
	// terminal state (logs were snapshotted at hold entry).
	if b.Annotations == nil {
		b.Annotations = map[string]string{}
	}
	b.Annotations[annPhase] = "cancelled"
	b.Annotations[annCompletedAt] = now
	p.archiveRecord(ctx, b, "cancelled")
	p.logger().Info("held build superseded — not promoted",
		"build", b.Name, "supersededBy", newer)
	if p.Notifier != nil {
		short := strings.TrimPrefix(b.Spec.Service, b.Spec.Project+"-")
		title, desc, fields := buildRichCard(b, short, "superseded", "", "")
		if desc == "" {
			desc = "Replaced by `" + newer + "` before its held promotion could complete"
		}
		p.Notifier.Emit(EventEnvelope{
			Type:        eventBuildSuperseded,
			Title:       title,
			Description: desc,
			Project:     b.Spec.Project,
			Service:     short,
			URL:         buildEventURL(b.Spec.Project, short),
			Severity:    "info",
			Fields:      fields,
		})
	}
	return nil
}
