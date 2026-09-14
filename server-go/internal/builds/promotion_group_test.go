package builds

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"kuso/server/internal/kube"
)

// The atomic same-repo promotion gate (the scubatony dc6ed19 incident:
// CMS rolled to a commit whose internal-system build failed → version
// mismatch between two halves of one codebase). These pin the decision
// matrix, especially the three deadlock-avoidance rules.

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func gb(name, service, ref, repoURL, phase string, created time.Time, annos map[string]string) kube.KusoBuild {
	a := map[string]string{}
	for k, v := range annos {
		a[k] = v
	}
	if phase != "" {
		a[annPhase] = phase
	}
	return kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			CreationTimestamp: metav1.NewTime(created),
			Annotations:       a,
		},
		Spec: kube.KusoBuildSpec{
			Project: "scuba",
			Service: service,
			Ref:     ref,
			Branch:  "main",
			Repo:    &kube.KusoRepoRef{URL: repoURL},
		},
	}
}

func TestPromotionHoldVerdict(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	repo := "https://github.com/acme/mono.git"
	// The build under decision: CMS build for shaA, Job just completed.
	cms := gb("cms-1", "scuba-cms", shaA, repo, "running", t0, nil)

	cases := []struct {
		name string
		all  []kube.KusoBuild
		want string // "" = proceed; else substring of the hold reason
	}{
		{
			name: "no siblings — solo service proceeds",
			all:  []kube.KusoBuild{cms},
			want: "",
		},
		{
			name: "sibling green for same sha — proceeds",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "succeeded", t0, nil)},
			want: "",
		},
		{
			name: "sibling still building — waits",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "running", t0, nil)},
			want: "waiting for sibling build",
		},
		{
			name: "sibling failed — holds",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil)},
			want: "sibling build failed",
		},
		{
			name: "sibling release-failed — holds (image unverified)",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "release-failed", t0, nil)},
			want: "sibling build failed",
		},
		{
			name: "sibling cancelled — operator skip, proceeds",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "cancelled", t0, nil)},
			want: "",
		},
		{
			name: "different repo sibling failing — unrelated, proceeds",
			all: []kube.KusoBuild{cms,
				gb("oth-1", "scuba-other", shaA, "https://github.com/acme/other", "failed", t0, nil)},
			want: "",
		},
		{
			name: "same repo but different sha — different wave, proceeds",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaB, repo, "failed", t0, nil)},
			want: "",
		},
		{
			// Deadlock rule 1: a sibling whose Job succeeded but is itself
			// held (annotation present, phase still non-terminal) counts
			// green — otherwise two simultaneous finishers wait forever.
			name: "sibling held-annotated non-terminal — counts green",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "running", t0,
					map[string]string{annPromoteHold: "waiting for sibling build: cms"})},
			want: "",
		},
		{
			// Deadlock rule 2 (forgiveness): the failed wave build's retry
			// carries a synthetic ref (not in the wave) and went green.
			name: "failed sibling forgiven by newer green retry",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil),
				gb("int-2", "scuba-internal", "main-retry", repo, "succeeded", t0.Add(5*time.Minute), nil)},
			want: "",
		},
		{
			name: "failed sibling with retry still running — waits on the retry",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil),
				gb("int-2", "scuba-internal", "main-retry", repo, "running", t0.Add(5*time.Minute), nil)},
			want: "waiting for sibling build",
		},
		{
			name: "failed sibling with failed retry — still held",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil),
				gb("int-2", "scuba-internal", "main-retry", repo, "failed", t0.Add(5*time.Minute), nil)},
			want: "sibling build failed",
		},
		{
			// Multiple wave builds for one sibling (webhook redelivery):
			// only the LATEST counts.
			name: "sibling redelivered build — latest (green) wins over older failed",
			all: []kube.KusoBuild{cms,
				gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil),
				gb("int-2", "scuba-internal", shaA, repo, "succeeded", t0.Add(2*time.Minute), nil)},
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := promotionHoldVerdict(&cms, c.all)
			if c.want == "" && got != "" {
				t.Errorf("want proceed, got hold: %q", got)
			}
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("want hold containing %q, got %q", c.want, got)
			}
		})
	}
}

// Manual/synthetic-ref builds bypass the gate entirely (the operator's
// escape hatch), and builds with no repo can't group.
func TestPromotionHoldVerdict_Bypasses(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	repo := "https://github.com/acme/mono"
	sibFailed := gb("int-1", "scuba-internal", "main-manual", repo, "failed", t0, nil)

	manual := gb("cms-1", "scuba-cms", "main-manual", repo, "running", t0, nil)
	if got := promotionHoldVerdict(&manual, []kube.KusoBuild{manual, sibFailed}); got != "" {
		t.Errorf("synthetic-ref build must bypass the gate, got %q", got)
	}

	noRepo := gb("cms-2", "scuba-cms", shaA, "", "running", t0, nil)
	noRepo.Spec.Repo = nil
	if got := promotionHoldVerdict(&noRepo, []kube.KusoBuild{noRepo}); got != "" {
		t.Errorf("repo-less build must bypass the gate, got %q", got)
	}
}

// Repo URLs that differ only in credentials, scheme case, .git suffix,
// or trailing slash are the SAME repo for grouping.
func TestNormalizePromoRepoURL(t *testing.T) {
	t.Parallel()
	want := "github.com/acme/mono"
	for _, in := range []string{
		"https://github.com/acme/Mono.git",
		"HTTPS://github.com/acme/mono/",
		"https://x:tok@github.com/acme/mono.git",
		"https://github.com/ACME/mono",
		// transport forms are the same repo
		"git@github.com:acme/mono.git",
		"ssh://git@github.com/acme/mono.git",
	} {
		if got := normalizePromoRepoURL(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// The supersede-break: a newer build for the held service (any state)
// releases the hold by superseding.
func TestNewerBuildOf(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	held := gb("cms-1", "scuba-cms", shaA, "r", "running", t0, nil)
	all := []kube.KusoBuild{
		held,
		gb("int-1", "scuba-internal", shaA, "r", "running", t0.Add(time.Hour), nil), // other service: ignored
		gb("cms-0", "scuba-cms", shaB, "r", "succeeded", t0.Add(-time.Hour), nil),   // older: ignored
	}
	if got := newerBuildOf(&held, all); got != "" {
		t.Errorf("no newer same-service build, got %q", got)
	}
	all = append(all, gb("cms-2", "scuba-cms", shaB, "r", "queued", t0.Add(time.Minute), nil))
	if got := newerBuildOf(&held, all); got != "cms-2" {
		t.Errorf("newerBuildOf = %q, want cms-2", got)
	}
}

// withBranch clones a test build onto a different branch — the shape
// preview builds take (real PR-head SHA, non-tracked branch).
func withBranch(b kube.KusoBuild, branch string) kube.KusoBuild {
	b.Spec.Branch = branch
	return b
}

// Preview/staging contamination: builds on OTHER branches must be
// invisible to all three rules — a failed preview build of a
// fast-forwarded SHA must not hold the production wave, a green
// preview build must not forgive a failed production sibling, and a
// newer preview build must not supersede a held production build.
func TestPromotionGate_BranchScoping(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	repo := "https://github.com/acme/mono"
	cms := gb("cms-1", "scuba-cms", shaA, repo, "running", t0, nil)

	// Failed PREVIEW build of the same SHA (fast-forward merge) —
	// different branch → not part of the production wave.
	preview := withBranch(gb("int-pr", "scuba-internal", shaA, repo, "failed", t0, nil), "feature-x")
	if got := promotionHoldVerdict(&cms, []kube.KusoBuild{cms, preview}); got != "" {
		t.Errorf("failed preview build must not hold the production wave, got %q", got)
	}

	// Failed production sibling + newer GREEN preview build: the
	// preview must NOT forgive the production failure.
	prodFail := gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil)
	previewGreen := withBranch(gb("int-pr2", "scuba-internal", shaB, repo, "succeeded", t0.Add(5*time.Minute), nil), "feature-x")
	if got := promotionHoldVerdict(&cms, []kube.KusoBuild{cms, prodFail, previewGreen}); !strings.Contains(got, "sibling build failed") {
		t.Errorf("green preview build must not forgive a failed production sibling, got %q", got)
	}

	// A newer PREVIEW build of the held service must not supersede it.
	held := gb("cms-held", "scuba-cms", shaA, repo, "running", t0, nil)
	newerPreview := withBranch(gb("cms-pr", "scuba-cms", shaB, repo, "running", t0.Add(time.Minute), nil), "feature-x")
	if got := newerBuildOf(&held, []kube.KusoBuild{held, newerPreview}); got != "" {
		t.Errorf("preview build must not supersede a held production build, got %q", got)
	}
	// ...but a newer build on the SAME branch does.
	newerMain := gb("cms-2", "scuba-cms", shaB, repo, "queued", t0.Add(2*time.Minute), nil)
	if got := newerBuildOf(&held, []kube.KusoBuild{held, newerPreview, newerMain}); got != "cms-2" {
		t.Errorf("same-branch successor must supersede, got %q", got)
	}
}

// The hold's time bound. A permanently-dead sibling (BackoffLimitExceeded,
// never retried) used to strand a GREEN build forever: the only escapes
// were a human retrying the sibling or pushing again. These pin the
// deadline and, just as importantly, the cases that must NOT expire.
func TestHoldExpired(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 14, 18, 0, 0, 0, time.UTC)
	stamp := func(v string) *kube.KusoBuild {
		b := gb("cms-1", "scuba-cms", shaA, "https://github.com/acme/mono.git", "running", now, nil)
		if v != "" {
			b.Annotations[annPromoteHoldSince] = v
		}
		return &b
	}

	cases := []struct {
		name  string
		since string
		want  bool
	}{
		// Absent stamp = first tick, or a CR predating the annotation.
		// Must not expire, or an upgrade would abandon live holds.
		{"no stamp never expires", "", false},
		// Unparseable must fail safe toward atomicity, not liveness.
		{"garbage stamp never expires", "not-a-timestamp", false},
		{"fresh hold holds", now.Add(-5 * time.Minute).Format(time.RFC3339), false},
		{"just under the bound holds", now.Add(-promoteHoldMaxAge + time.Minute).Format(time.RFC3339), false},
		{"exactly at the bound expires", now.Add(-promoteHoldMaxAge).Format(time.RFC3339), true},
		{"well past the bound expires", now.Add(-24 * time.Hour).Format(time.RFC3339), true},
		// A clock skew / future stamp must not expire instantly.
		{"future stamp holds", now.Add(time.Hour).Format(time.RFC3339), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := holdExpired(stamp(tc.since), now); got != tc.want {
				t.Errorf("holdExpired(%q) = %v, want %v", tc.since, got, tc.want)
			}
		})
	}
}

// notePromotionHold must stamp the since-time exactly once, at hold
// entry, and must NOT re-stamp it when only the hold REASON changes.
// A wave whose reason flaps (one sibling fails, then another) is still
// one continuous wait; re-stamping would reset the deadline forever and
// resurrect the very stall the bound exists to end.
//
// Drives the real function against the fake dynamic client and asserts
// on what lands on the CR — the earlier version of this test asserted on
// a map it had built itself, so it could not have caught a regression in
// this control flow.
func TestNotePromotionHoldStampsSinceOnce(t *testing.T) {
	t.Parallel()
	b := gb("cms-1", "scuba-cms", shaA, "https://github.com/acme/mono.git", "running", time.Now(), nil)
	b.Namespace = "kuso"
	svc := fakeService(t, seedBuild(&b))
	p := &Poller{Svc: svc}
	ctx := context.Background()

	read := func() map[string]string {
		t.Helper()
		raw, err := svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").
			Get(ctx, "cms-1", metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get build: %v", err)
		}
		return raw.GetAnnotations()
	}

	p.notePromotionHold(ctx, "kuso", &b, "waiting for sibling build: internal (int-1)")
	first := read()
	stamp := first[annPromoteHoldSince]
	if stamp == "" {
		t.Fatal("first hold must stamp the since-time; without it holdExpired never fires and the bound is dead code")
	}
	if _, err := time.Parse(time.RFC3339, stamp); err != nil {
		t.Fatalf("since-stamp must be RFC3339 (holdExpired parses it): %q: %v", stamp, err)
	}

	// Same reason again: the early-return should make this a no-op.
	p.notePromotionHold(ctx, "kuso", &b, "waiting for sibling build: internal (int-1)")
	if got := read()[annPromoteHoldSince]; got != stamp {
		t.Errorf("repeat hold with same reason changed the stamp: %q -> %q", stamp, got)
	}

	// Reason CHANGES: the hold annotation must update, the stamp must not.
	//
	// Backdate the stamp first. time.Now() at RFC3339 second-granularity
	// renders an identical string twice within the same second, so a
	// re-stamp would be invisible against a fresh stamp — verified: the
	// naive form of this assertion passes even when the guard is removed.
	// An unmistakably old value makes the regression detectable.
	old := time.Now().UTC().Add(-90 * time.Minute).Format(time.RFC3339)
	if _, err := svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Patch(
		ctx, "cms-1", types.MergePatchType,
		[]byte(fmt.Sprintf(`{"metadata":{"annotations":{%q:%q}}}`, annPromoteHoldSince, old)),
		metav1.PatchOptions{},
	); err != nil {
		t.Fatalf("backdate stamp: %v", err)
	}
	b.Annotations[annPromoteHoldSince] = old

	p.notePromotionHold(ctx, "kuso", &b, "sibling build failed: internal (int-1)")
	after := read()
	if after[annPromoteHold] != "sibling build failed: internal (int-1)" {
		t.Errorf("hold reason did not update: %q", after[annPromoteHold])
	}
	if after[annPromoteHoldSince] != old {
		t.Errorf("reason change re-stamped the deadline (%q -> %q) — a flapping reason would hold forever",
			old, after[annPromoteHoldSince])
	}
}

// clearPromotionHold must remove BOTH annotations. Leaving a stale
// since-stamp behind would make the NEXT hold on this build inherit an
// old deadline and expire immediately.
func TestClearPromotionHoldRemovesSinceStamp(t *testing.T) {
	t.Parallel()
	b := gb("cms-1", "scuba-cms", shaA, "https://github.com/acme/mono.git", "running", time.Now(), nil)
	b.Namespace = "kuso"
	svc := fakeService(t, seedBuild(&b))
	p := &Poller{Svc: svc}
	ctx := context.Background()

	p.notePromotionHold(ctx, "kuso", &b, "waiting for sibling build: internal (int-1)")
	p.clearPromotionHold(ctx, "kuso", &b)

	raw, err := svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").
		Get(ctx, "cms-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	a := raw.GetAnnotations()
	if a[annPromoteHold] != "" {
		t.Errorf("hold annotation survived clear: %q", a[annPromoteHold])
	}
	if a[annPromoteHoldSince] != "" {
		t.Errorf("since-stamp survived clear: %q — the next hold would inherit a stale deadline", a[annPromoteHoldSince])
	}
	if b.Annotations[annPromoteHoldSince] != "" {
		t.Errorf("in-memory since-stamp survived clear: %q", b.Annotations[annPromoteHoldSince])
	}
}

// A build already sitting in a hold when this bound shipped carries a
// hold annotation but no since-stamp. It must be backfilled on the next
// tick — otherwise firstHold stays false forever, holdExpired reads the
// absent stamp as "not expired", and the builds this bound exists to
// rescue stay stuck for life. Reproduces the upgrade boundary: seed a
// held build with NO stamp, then tick the gate with the SAME reason
// (the steady-state path that early-returns).
func TestNotePromotionHoldBackfillsStampOnUpgrade(t *testing.T) {
	t.Parallel()
	const reason = "sibling build failed: internal (int-1)"
	b := gb("cms-1", "scuba-cms", shaA, "https://github.com/acme/mono.git", "running", time.Now(), nil)
	b.Namespace = "kuso"
	b.Annotations[annPromoteHold] = reason // held before the upgrade
	svc := fakeService(t, seedBuild(&b))
	p := &Poller{Svc: svc}
	ctx := context.Background()

	if b.Annotations[annPromoteHoldSince] != "" {
		t.Fatal("precondition: the pre-upgrade build must have no since-stamp")
	}

	// Same reason as the existing hold — the steady-state no-op path.
	p.notePromotionHold(ctx, "kuso", &b, reason)

	raw, err := svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").
		Get(ctx, "cms-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	got := raw.GetAnnotations()[annPromoteHoldSince]
	if got == "" {
		t.Fatal("pre-existing hold was not backfilled with a since-stamp; it can never expire")
	}
	if _, err := time.Parse(time.RFC3339, got); err != nil {
		t.Fatalf("backfilled stamp must be RFC3339: %q: %v", got, err)
	}
	if b.Annotations[annPromoteHoldSince] != got {
		t.Errorf("in-memory stamp %q disagrees with the CR %q", b.Annotations[annPromoteHoldSince], got)
	}

	// And the backfill must be idempotent — a second identical tick
	// must not re-stamp, or the deadline resets every 5s forever.
	p.notePromotionHold(ctx, "kuso", &b, reason)
	raw2, err := svc.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").
		Get(ctx, "cms-1", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	if got2 := raw2.GetAnnotations()[annPromoteHoldSince]; got2 != got {
		t.Errorf("backfill is not idempotent: %q -> %q; the deadline would never be reached", got, got2)
	}
}
