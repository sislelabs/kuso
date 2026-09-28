package builds

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const headSHA = "46fe4156ba59e4cd0afd264febce9890068f253d"

// fakeGithub stands in for *github.Client: it preflights repo access and
// resolves a branch head, recording what it was asked.
type fakeGithub struct {
	sha    string
	err    error
	calls  int
	instID int64
	owner  string
	repo   string
	branch string
}

func (f *fakeGithub) CheckRepoAccess(context.Context, int64, string, string) error { return nil }

func (f *fakeGithub) ResolveBranchSHA(_ context.Context, installationID int64, owner, repo, branch string) (string, error) {
	f.calls++
	f.instID, f.owner, f.repo, f.branch = installationID, owner, repo, branch
	return f.sha, f.err
}

type fakeInstallResolver struct{ id int64 }

func (f fakeInstallResolver) ResolveInstallationForRepo(context.Context, string, string) (int64, error) {
	return f.id, nil
}

// The e2e incident: the project had no installation bound and the
// service's repo was the project defaultRepo. The installation is only
// discoverable through the resolver cache — a manual trigger must still
// land the branch HEAD SHA, not a synthetic "<branch>-<ms>" ref.
func TestCreate_ManualTrigger_ResolvesBranchHeadViaAutoInstallation(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 0),
		seedService("e2e", "api"),
	)
	gh := &fakeGithub{sha: headSHA}
	s.RepoAccess = gh
	s.InstallResolver = fakeInstallResolver{id: 128668920}

	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "staging"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Spec.Ref != headSHA {
		t.Errorf("spec.ref = %q, want resolved HEAD %q", got.Spec.Ref, headSHA)
	}
	if !isBranchHeadBuild(got) {
		t.Error("resolved manual build must carry the branch-head marker")
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != headSHA[:12] {
		t.Errorf("image tag = %+v, want %q", got.Spec.Image, headSHA[:12])
	}
	if gh.instID != 128668920 || gh.owner != "example" || gh.repo != "api" || gh.branch != "staging" {
		t.Errorf("resolver asked (%d, %s/%s@%s)", gh.instID, gh.owner, gh.repo, gh.branch)
	}
}

func TestCreate_ManualTrigger_FallsBackToSyntheticWhenResolveFails(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 128668920),
		seedService("e2e", "api"),
	)
	s.RepoAccess = &fakeGithub{err: errors.New("github: get branch: 404")}

	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "staging"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(got.Spec.Ref, "staging-") || shaRE.MatchString(got.Spec.Ref) {
		t.Errorf("spec.ref = %q, want synthetic staging-<ms>", got.Spec.Ref)
	}
}

// An explicit ref (webhook path) must never be second-guessed by a
// branch lookup.
func TestCreate_ExplicitRefSkipsBranchResolve(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 128668920),
		seedService("e2e", "api"),
	)
	gh := &fakeGithub{sha: headSHA}
	s.RepoAccess = gh
	const explicit = "abcdef0123456789abcdef0123456789abcdef01"
	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "main", Ref: explicit})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Spec.Ref != explicit || gh.calls != 0 || isBranchHeadBuild(got) {
		t.Errorf("ref=%q resolveCalls=%d marker=%v, want explicit ref, no lookup, no marker", got.Spec.Ref, gh.calls, isBranchHeadBuild(got))
	}
}

// Redeploying an unchanged HEAD is the common case: a webhook build for
// that SHA already owns the deterministic <project>-<svc>-<sha12> name.
// The manual trigger must still create its own CR, not 500 on
// AlreadyExists.
func TestCreate_ManualRedeployOfBuiltSHADoesNotCollide(t *testing.T) {
	t.Parallel()
	prior := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:      buildCRName("e2e", "api", headSHA),
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject:                "e2e",
				kube.LabelService:                "e2e-api",
				"kuso.sislelabs.com/build-state": "done",
			},
			Annotations: map[string]string{annPhase: "succeeded", annTriggerSource: "webhook"},
		},
		Spec: kube.KusoBuildSpec{Project: "e2e", Service: "e2e-api", Ref: headSHA, Branch: "main"},
	}
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 128668920),
		seedService("e2e", "api"),
		seedBuild(prior),
	)
	s.RepoAccess = &fakeGithub{sha: headSHA}

	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "main", TriggeredBy: "user"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Name == prior.Name {
		t.Fatalf("manual redeploy reused the webhook build's name %q", got.Name)
	}
	if got.Spec.Ref != headSHA {
		t.Errorf("spec.ref = %q, want %q", got.Spec.Ref, headSHA)
	}
}

// Two manual triggers past the coalesce window, same HEAD, must both
// create (distinct CR names) rather than collide.
func TestCreate_TwoManualTriggersSameHEADPastWindow(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 128668920),
		seedService("e2e", "api"),
	)
	s.RepoAccess = &fakeGithub{sha: headSHA}
	first, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	backdate(t, s, first.Name, 2*time.Minute)
	time.Sleep(5 * time.Millisecond)
	second, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Name == second.Name {
		t.Errorf("both triggers produced %q", first.Name)
	}
}

// #29: the caller must be able to tell "created" from "returned the
// in-flight one".
func TestCreateWithOutcome_ReportsCoalesced(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("alpha", "main", "https://github.com/example/alpha", 0),
		seedService("alpha", "web"),
	)
	first, err := s.CreateWithOutcome(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.Existing {
		t.Error("first trigger reported as existing")
	}
	second, err := s.CreateWithOutcome(context.Background(), "alpha", "web", CreateBuildRequest{})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if !second.Existing || second.Build.Name != first.Build.Name {
		t.Errorf("second: existing=%v name=%q, want existing=true name=%q", second.Existing, second.Build.Name, first.Build.Name)
	}
}

// A manual trigger that resolves HEAD must not be folded into an
// in-flight build of a DIFFERENT commit — that's a different intent.
func TestCreate_ManualDoesNotCoalesceIntoDifferentCommit(t *testing.T) {
	t.Parallel()
	const olderSHA = "1111111111111111111111111111111111111111"
	inflight := &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name:              buildCRName("e2e", "api", olderSHA),
			Namespace:         "kuso",
			CreationTimestamp: metav1.NewTime(time.Now()),
			Labels: map[string]string{
				kube.LabelProject: "e2e",
				kube.LabelService: "e2e-api",
			},
			Annotations: map[string]string{annPhase: "running", annTriggerSource: "webhook"},
		},
		Spec: kube.KusoBuildSpec{Project: "e2e", Service: "e2e-api", Ref: olderSHA, Branch: "main"},
	}
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 128668920),
		seedService("e2e", "api"),
		seedBuild(inflight),
	)
	s.RepoAccess = &fakeGithub{sha: headSHA}
	out, err := s.CreateWithOutcome(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "main"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if out.Existing || out.Build.Name == inflight.Name {
		t.Errorf("coalesced into older-commit build %q", out.Build.Name)
	}
}

// Manual triggers resolved to the branch HEAD keep the promotion-gate
// escape hatch the synthetic ref used to provide.
func TestPromotionHoldVerdict_ManualRealSHABypasses(t *testing.T) {
	t.Parallel()
	t0 := time.Now()
	repo := "https://github.com/acme/mono"
	sibFailed := gb("int-1", "scuba-internal", shaA, repo, "failed", t0, nil)
	manual := gb("cms-1", "scuba-cms", shaA, repo, "running", t0, map[string]string{annRefFromBranch: "true"})
	if got := promotionHoldVerdict(&manual, []kube.KusoBuild{manual, sibFailed}); got != "" {
		t.Errorf("manual real-SHA build must bypass the gate, got %q", got)
	}
	// ...and a manual sibling doesn't join a webhook build's wave.
	webhook := gb("cms-2", "scuba-cms", shaA, repo, "running", t0, map[string]string{annTriggerSource: "webhook"})
	manualSibFailed := gb("int-2", "scuba-internal", shaA, repo, "failed", t0, map[string]string{annRefFromBranch: "true"})
	if got := promotionHoldVerdict(&webhook, []kube.KusoBuild{webhook, manualSibFailed}); got != "" {
		t.Errorf("manual sibling must not hold the webhook wave, got %q", got)
	}
}

// Manual rebuilds of one commit share an image tag. Untagging the older
// record would delete the tag the kept (newer) record still points at.
func TestImagesToUntag_SharedTagHeldByKeptRecord(t *testing.T) {
	t.Parallel()
	mk := func(name, tag string, sec int) imageRetentionRecord {
		return imageRetentionRecord{buildName: name, service: "api", imageTag: tag, createdAt: ts(sec), succeeded: true}
	}
	byKey := map[svcKey][]imageRetentionRecord{
		{"e2e", "api"}: {
			mk("b-new", "46fe4156ba59", 100),
			mk("b-mid", "t2", 90),
			mk("b-old", "46fe4156ba59", 80),
		},
	}
	for _, g := range imagesToUntag(byKey, 2, nil) {
		if g.tag == "46fe4156ba59" {
			t.Errorf("untagged %s, still held by kept record b-new", g.tag)
		}
	}
}

func backdate(t *testing.T, s *Service, name string, by time.Duration) {
	t.Helper()
	raw, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get %s: %v", name, err)
	}
	raw.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-by)))
	if _, err := s.Kube.Dynamic.Resource(kube.GVRBuilds).Namespace("kuso").Update(context.Background(), raw, metav1.UpdateOptions{}); err != nil {
		t.Fatalf("backdate %s: %v", name, err)
	}
}
