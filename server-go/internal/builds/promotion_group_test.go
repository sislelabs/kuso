package builds

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
