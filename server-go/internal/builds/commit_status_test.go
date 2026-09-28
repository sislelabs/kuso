package builds

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/kube"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

type fakeStatusReporter struct {
	mu    sync.Mutex
	posts []CommitStatus
	err   error
}

func (f *fakeStatusReporter) PostCommitStatus(_ context.Context, s CommitStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.posts = append(f.posts, s)
	return nil
}

func (f *fakeStatusReporter) snapshot() []CommitStatus {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CommitStatus(nil), f.posts...)
}

func githubBuild(name, branch, phase string, extra map[string]string) *kube.KusoBuild {
	annos := map[string]string{}
	if phase != "" {
		annos[annPhase] = phase
	}
	for k, v := range extra {
		annos[k] = v
	}
	labels := map[string]string{
		kube.LabelProject: "shop",
		kube.LabelService: "shop-web",
	}
	switch phase {
	case "queued":
		labels[LabelBuildState] = "queued"
	case "succeeded", "failed", "cancelled", "release-failed":
		labels[LabelBuildState] = BuildStateDone
	}
	return &kube.KusoBuild{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "kuso", Labels: labels, Annotations: annos,
			CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute)),
		},
		Spec: kube.KusoBuildSpec{
			Project:              "shop",
			Service:              "shop-web",
			Ref:                  testSHA,
			Branch:               branch,
			Repo:                 &kube.KusoRepoRef{URL: "https://github.com/acme/shop"},
			GithubInstallationID: 42,
		},
	}
}

func seedEnv(project, service, env, branch string) seed {
	e := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      project + "-" + service + "-" + env,
			Namespace: "kuso",
			Labels: map[string]string{
				kube.LabelProject: project,
				kube.LabelService: service,
				kube.LabelEnv:     env,
			},
		},
		Spec: kube.KusoEnvironmentSpec{Project: project, Service: project + "-" + service, Branch: branch},
	}
	return typedSeed(kube.GVREnvironments, "KusoEnvironment", e)
}

// setPhase rewrites a seeded build's phase in the fake cluster the way
// the real terminal paths do (annotation + done label).
func setPhase(t *testing.T, svc *Service, name, phase string, extra map[string]string) {
	t.Helper()
	b, err := svc.Kube.GetKusoBuild(context.Background(), "kuso", name)
	if err != nil {
		t.Fatalf("get build: %v", err)
	}
	fresh := githubBuild(name, b.Spec.Branch, phase, nil)
	for k, v := range b.Annotations {
		if k != annPhase {
			fresh.Annotations[k] = v
		}
	}
	for k, v := range extra {
		fresh.Annotations[k] = v
	}
	fresh.Spec = b.Spec
	if err := svc.Kube.DeleteKusoBuild(context.Background(), "kuso", name); err != nil {
		t.Fatalf("delete build: %v", err)
	}
	if _, err := svc.Kube.CreateKusoBuild(context.Background(), "kuso", fresh); err != nil {
		t.Fatalf("recreate build: %v", err)
	}
}

func syncOnce(t *testing.T, p *Poller) {
	t.Helper()
	raw, err := p.Svc.Kube.ListKusoBuildsByLabels(context.Background(), "kuso", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	p.syncCommitStatuses(context.Background(), "kuso", raw)
	p.waitDetached()
}

func TestCommitStatus_PostedOnEachTransition(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedProductionEnv("shop", "web"),
		seedBuild(githubBuild("b1", "main", "queued", nil)),
	)
	rep := &fakeStatusReporter{}
	p := &Poller{Svc: svc, CommitStatuses: rep}

	syncOnce(t, p)
	syncOnce(t, p) // unchanged phase → no duplicate post
	setPhase(t, svc, "b1", "running", nil)
	syncOnce(t, p)
	setPhase(t, svc, "b1", "succeeded", nil)
	syncOnce(t, p)
	syncOnce(t, p)

	posts := rep.snapshot()
	want := []struct{ state, descPrefix string }{
		{"pending", "Queued"},
		{"pending", "Building"},
		{"success", "Deployed"},
	}
	if len(posts) != len(want) {
		t.Fatalf("posts = %d (%+v), want %d", len(posts), posts, len(want))
	}
	for i, w := range want {
		got := posts[i]
		if got.State != w.state || !strings.HasPrefix(got.Description, w.descPrefix) {
			t.Errorf("post %d: state=%q desc=%q, want %q/%q…", i, got.State, got.Description, w.state, w.descPrefix)
		}
		if got.Context != "kuso/web" {
			t.Errorf("post %d: context %q, want kuso/web", i, got.Context)
		}
		if got.Owner != "acme" || got.Repo != "shop" || got.SHA != testSHA || got.InstallationID != 42 {
			t.Errorf("post %d: target %s/%s@%s inst=%d", i, got.Owner, got.Repo, got.SHA, got.InstallationID)
		}
		if got.TargetPath != "/projects/shop?service=web&tab=deployments" {
			t.Errorf("post %d: target path %q", i, got.TargetPath)
		}
	}
	b, _ := svc.Kube.GetKusoBuild(context.Background(), "kuso", "b1")
	if b.Annotations[annCommitStatus] != "success" {
		t.Errorf("posted-state annotation = %q, want success", b.Annotations[annCommitStatus])
	}
}

func TestCommitStatus_NonProductionContextCarriesEnv(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedProductionEnv("shop", "web"),
		seedEnv("shop", "web", "staging", "develop"),
		seedBuild(githubBuild("b1", "develop", "running", nil)),
	)
	rep := &fakeStatusReporter{}
	p := &Poller{Svc: svc, CommitStatuses: rep}
	syncOnce(t, p)
	// The context is stamped on first post and reused, so a later
	// transition keeps the same context even if env lookup would differ.
	setPhase(t, svc, "b1", "failed", map[string]string{annMessage: "exit 1"})
	syncOnce(t, p)

	posts := rep.snapshot()
	if len(posts) != 2 {
		t.Fatalf("posts = %+v", posts)
	}
	for _, s := range posts {
		if s.Context != "kuso/web (staging)" {
			t.Errorf("context %q, want kuso/web (staging)", s.Context)
		}
		if s.TargetPath != "/projects/shop?service=web&tab=deployments&env=staging" {
			t.Errorf("target path %q", s.TargetPath)
		}
	}
	if posts[1].State != "failure" {
		t.Errorf("failed build posted %q, want failure", posts[1].State)
	}
}

func TestDesiredCommitStatus(t *testing.T) {
	long := strings.Repeat("x", 400)
	cases := []struct {
		name       string
		phase      string
		annos      map[string]string
		wantState  string
		wantPrefix string
	}{
		{"ci gate waiting", "queued", map[string]string{annCIGate: ciGateWaiting}, "pending", "Waiting for CI"},
		{"queued", "queued", nil, "pending", "Queued"},
		{"no phase yet", "", nil, "pending", "Building"},
		{"pending", "pending", nil, "pending", "Building"},
		{"held", "running", map[string]string{annPromoteHold: "waiting on api"}, "pending", "Built"},
		{"succeeded", "succeeded", nil, "success", "Deployed"},
		{"failed", "failed", map[string]string{annMessage: long}, "failure", "Build failed"},
		{"release failed", "release-failed", map[string]string{annMessage: "migrate\nboom: relation missing"}, "failure", "Release hook failed"},
		{"ci failed cancel", "cancelled", map[string]string{annCIGate: ciGateFailed, annMessage: "CI failed: lint"}, "failure", "CI failed: lint"},
		{"ci timeout cancel", "cancelled", map[string]string{annCIGate: ciGateTimedOut, annMessage: "CI checks did not finish"}, "failure", "CI checks did not finish"},
		{"user cancel", "cancelled", map[string]string{annMessage: "cancelled by user"}, "error", "Cancelled"},
		{"superseded", "cancelled", map[string]string{annSupersededBy: "b2", annMessage: "superseded by b2"}, "error", "Superseded"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := githubBuild("b", "main", c.phase, c.annos)
			key, state, desc := desiredCommitStatus(b)
			if state != c.wantState || !strings.HasPrefix(desc, c.wantPrefix) {
				t.Errorf("got %q/%q, want %q/%q…", state, desc, c.wantState, c.wantPrefix)
			}
			if key == "" {
				t.Error("empty key")
			}
			if n := utf8.RuneCountInString(desc); n > 140 {
				t.Errorf("description is %d runes, GitHub caps at 140", n)
			}
			if strings.Contains(desc, "\n") {
				t.Errorf("description must be one line: %q", desc)
			}
		})
	}
}

func TestCommitStatus_SkipsIneligibleBuilds(t *testing.T) {
	synthetic := githubBuild("synthetic", "main", "running", nil)
	synthetic.Spec.Ref = "main-mp81chv5"
	gitlab := githubBuild("gitlab", "main", "running", nil)
	gitlab.Spec.Repo.URL = "https://gitlab.com/acme/shop"
	noInstall := githubBuild("noinstall", "main", "running", nil)
	noInstall.Spec.GithubInstallationID = 0
	old := githubBuild("old", "main", "succeeded", map[string]string{
		annCompletedAt: time.Now().Add(-3 * time.Hour).UTC().Format(time.RFC3339),
	})
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedBuild(synthetic), seedBuild(gitlab), seedBuild(noInstall), seedBuild(old),
	)
	rep := &fakeStatusReporter{}
	p := &Poller{Svc: svc, CommitStatuses: rep}
	syncOnce(t, p)
	if posts := rep.snapshot(); len(posts) != 0 {
		t.Fatalf("expected no posts, got %+v", posts)
	}
}

func TestCommitStatus_PostFailureIsRetriedNotStamped(t *testing.T) {
	svc := fakeService(t,
		seedProject("shop", "main", "https://github.com/acme/shop", 42),
		seedBuild(githubBuild("b1", "main", "running", nil)),
	)
	rep := &fakeStatusReporter{err: errors.New("403 resource not accessible by integration")}
	p := &Poller{Svc: svc, CommitStatuses: rep}
	syncOnce(t, p)
	b, _ := svc.Kube.GetKusoBuild(context.Background(), "kuso", "b1")
	if v := b.Annotations[annCommitStatus]; v != "" {
		t.Fatalf("failed post must not stamp the posted-state annotation, got %q", v)
	}
	// Backoff: an immediate re-sync does not hammer GitHub.
	rep.mu.Lock()
	rep.err = nil
	rep.mu.Unlock()
	syncOnce(t, p)
	if posts := rep.snapshot(); len(posts) != 0 {
		t.Fatalf("post retried inside the backoff window: %+v", posts)
	}
	// Once the backoff lapses it posts.
	p.statusMu.Lock()
	for k, r := range p.statusRetry {
		r.next = time.Time{}
		p.statusRetry[k] = r
	}
	p.statusMu.Unlock()
	syncOnce(t, p)
	if posts := rep.snapshot(); len(posts) != 1 || posts[0].State != "pending" {
		t.Fatalf("expected one pending post after backoff, got %+v", posts)
	}
}
