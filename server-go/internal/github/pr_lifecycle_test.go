package github

import (
	"context"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

type recordingPreviewDB struct {
	mu      sync.Mutex
	deleted []int
}

func (*recordingPreviewDB) EnsurePRAddons(context.Context, string, int) ([]string, map[string]string, error) {
	return nil, nil, nil
}

func (r *recordingPreviewDB) DeletePRAddons(_ context.Context, _ string, n int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deleted = append(r.deleted, n)
	return nil
}

func (r *recordingPreviewDB) calls() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.deleted)
}

const sha40 = "abcdef0123456789abcdef0123456789abcdef01"

func prBody(action, repo, headRepo, headRef string, number int) []byte {
	return []byte(`{
		"action": "` + action + `",
		"number": ` + strconv.Itoa(number) + `,
		"pull_request": {
			"head": {"ref": "` + headRef + `", "sha": "` + sha40 + `", "repo": {"full_name": "` + headRepo + `"}},
			"base": {"ref": "main", "repo": {"full_name": "` + repo + `"}},
			"state": "open", "author_association": "OWNER"
		},
		"repository": {"full_name": "` + repo + `"}
	}`)
}

func envExists(d *Dispatcher, name string) bool {
	_, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", name)
	return err == nil
}

func buildPhases(t *testing.T, d *Dispatcher, service string) []string {
	t.Helper()
	bs, err := d.Builds.List(context.Background(), "alpha", service)
	if err != nil {
		t.Fatalf("list builds: %v", err)
	}
	var out []string
	for _, b := range bs {
		out = append(out, b.Annotations[builds.AnnBuildPhase])
	}
	return out
}

// BLD-2: an untrusted fork PR whose head is `main` used to cancel every
// in-flight production `main` build when it was closed.
func TestPRClose_ForkPRDoesNotCancelProductionBuild(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	ctx := context.Background()
	push := []byte(`{"ref": "refs/heads/main", "after": "` + sha40 + `",
		"repository": {"full_name": "example/alpha", "default_branch": "main"}}`)
	if err := d.Dispatch(ctx, "push", push); err != nil {
		t.Fatalf("push: %v", err)
	}
	if err := d.Dispatch(ctx, "pull_request", prBody("closed", "example/alpha", "mallory/alpha", "main", 9)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if got := buildPhases(t, d, "web"); slices.Contains(got, "cancelled") {
		t.Fatalf("closing a fork PR cancelled the production build: phases %v", got)
	}
}

// BLD-3: PR numbers are per repo. Closing web#5 must not tear down api#5's
// preview, nor drop the per-PR clones it still uses.
func TestPRClose_MultiRepoLeavesOtherRepoPreview(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/web", "main", true, 5),
		seedSvc("alpha", "web"),
		seedSvcWithRepo("alpha", "api", "https://github.com/example/api", nil),
		seedPreviewEnv("alpha", "web", 5, "feat/w"),
		seedPreviewEnv("alpha", "api", 5, "feat/a"),
	)
	rec := &recordingPreviewDB{}
	d.PreviewDB = rec
	ctx := context.Background()

	if err := d.Dispatch(ctx, "pull_request", prBody("closed", "example/web", "example/web", "feat/w", 5)); err != nil {
		t.Fatalf("close web#5: %v", err)
	}
	if envExists(d, "alpha-web-pr-5") {
		t.Error("web#5 preview survived its own close")
	}
	if !envExists(d, "alpha-api-pr-5") {
		t.Fatal("closing web#5 deleted api#5's preview")
	}
	if got := rec.calls(); len(got) != 0 {
		t.Fatalf("clones dropped while api#5 still uses them: DeletePRAddons%v", got)
	}

	if err := d.Dispatch(ctx, "pull_request", prBody("closed", "example/api", "example/api", "feat/a", 5)); err != nil {
		t.Fatalf("close api#5: %v", err)
	}
	if envExists(d, "alpha-api-pr-5") {
		t.Error("api#5 preview survived its own close")
	}
	if got := rec.calls(); !slices.Equal(got, []int{5}) {
		t.Errorf("DeletePRAddons calls = %v, want [5] once the last #5 preview is gone", got)
	}
}

// BLD-14: closing must tear the preview down even after previews were
// switched off or the PR was retargeted off a trigger branch.
func TestPRClose_TearsDownAfterPreviewsDisabled(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", false, 5),
		seedSvc("alpha", "web"),
		seedPreviewEnv("alpha", "web", 3, "feat/x"),
	)
	if err := d.Dispatch(context.Background(), "pull_request", prBody("closed", "example/alpha", "example/alpha", "feat/x", 3)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if envExists(d, "alpha-web-pr-3") {
		t.Error("preview leaked: close skipped because previews are now disabled")
	}
}

func TestPRClose_TearsDownAfterRetargetOffTrigger(t *testing.T) {
	t.Parallel()
	proj := &kube.KusoProject{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha", Namespace: "kuso"},
		Spec: kube.KusoProjectSpec{
			DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/example/alpha", DefaultBranch: "main"},
			BaseDomain:  "alpha.example.com",
			Previews: &kube.KusoPreviewsSpec{Enabled: true, Triggers: []kube.KusoPreviewTrigger{
				{Branch: "develop", BaseEnv: "production"},
			}},
		},
	}
	d := newDispatcher(t,
		typedSeed(kube.GVRProjects, "KusoProject", proj),
		seedSvc("alpha", "web"),
		seedPreviewEnv("alpha", "web", 4, "feat/x"),
	)
	// prBody's base ref is "main", which is not a trigger branch.
	if err := d.Dispatch(context.Background(), "pull_request", prBody("closed", "example/alpha", "example/alpha", "feat/x", 4)); err != nil {
		t.Fatalf("close: %v", err)
	}
	if envExists(d, "alpha-web-pr-4") {
		t.Error("preview leaked: close skipped because the base branch is no longer a trigger")
	}
}

// BLD-13: close cancels the SHA-keyed build; on reopen that dead build used
// to block a rebuild of the same commit, leaving the preview with no image.
func TestPRReopen_RebuildsCancelledBuild(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	ctx := context.Background()
	for _, action := range []string{"opened", "closed", "reopened"} {
		if err := d.Dispatch(ctx, "pull_request", prBody(action, "example/alpha", "example/alpha", "feat/x", 42)); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	phases := buildPhases(t, d, "web")
	live := 0
	for _, p := range phases {
		if p != "cancelled" && p != "failed" {
			live++
		}
	}
	if live != 1 {
		t.Fatalf("after reopen want exactly one live rebuild of the commit, phases = %v", phases)
	}
}

// BLD-15 / PERF-3: a synchronize processed after the close must not
// recreate the preview; a reopen lifts that.
func TestPRSynchronizeAfterClose_DoesNotResurrectPreview(t *testing.T) {
	t.Parallel()
	d := newDispatcher(t,
		seedProj("alpha", "https://github.com/example/alpha", "main", true, 5),
		seedSvc("alpha", "web"),
	)
	ctx := context.Background()
	for _, action := range []string{"opened", "closed", "synchronize"} {
		if err := d.Dispatch(ctx, "pull_request", prBody(action, "example/alpha", "example/alpha", "feat/x", 8)); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
	}
	if envExists(d, "alpha-web-pr-8") {
		t.Fatal("synchronize after close recreated the preview")
	}
	if err := d.Dispatch(ctx, "pull_request", prBody("reopened", "example/alpha", "example/alpha", "feat/x", 8)); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !envExists(d, "alpha-web-pr-8") {
		t.Fatal("reopen did not bring the preview back")
	}
}

// BLD-4: previews must not mount the production service secret, neither on
// create nor carried across a resync, nor read it per key.
func TestPreview_DoesNotMountServiceSecret(t *testing.T) {
	t.Parallel()
	d := e2eShapedDispatcher(t, []string{"SHARED_TOKEN"})
	env := dispatchPR7(t, d)
	if slices.Contains(env.Spec.EnvFromSecrets, "alpha-api-secrets") {
		t.Fatalf("new preview mounts the production service secret: %v", env.Spec.EnvFromSecrets)
	}

	stale := &kube.KusoEnvironment{
		ObjectMeta: metav1.ObjectMeta{Name: "alpha-api-pr-8", Namespace: "kuso",
			Labels: map[string]string{kube.LabelProject: "alpha", kube.LabelService: "api"}},
		Spec: kube.KusoEnvironmentSpec{
			Project: "alpha", Service: "alpha-api", Kind: "preview",
			EnvFromSecrets: []string{"alpha-api-secrets", "alpha-api-pr-8-secrets"},
			PullRequest:    &kube.KusoPullRequest{Number: 8},
		},
	}
	if _, err := d.Kube.CreateKusoEnvironment(context.Background(), "kuso", stale); err != nil {
		t.Fatalf("seed stale preview: %v", err)
	}
	if err := d.Dispatch(context.Background(), "pull_request", prBody("synchronize", "example/alpha", "example/alpha", "feat/x", 8)); err != nil {
		t.Fatalf("synchronize: %v", err)
	}
	got, err := d.Kube.GetKusoEnvironment(context.Background(), "kuso", "alpha-api-pr-8")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if slices.Contains(got.Spec.EnvFromSecrets, "alpha-api-secrets") {
		t.Fatalf("resync kept the production service secret: %v", got.Spec.EnvFromSecrets)
	}

	vars := dropSecretKeyRefsTo([]kube.KusoEnvVar{
		{Name: "STRIPE_KEY", ValueFrom: map[string]any{"secretKeyRef": map[string]any{"name": "alpha-api-secrets", "key": "STRIPE_KEY"}}},
		{Name: "PLAIN", Value: "x"},
	}, "alpha-api-secrets")
	if len(vars) != 1 || vars[0].Name != "PLAIN" {
		t.Errorf("per-key ref into the service secret kept: %+v", vars)
	}
}

func TestKeyedSerializer_RunsSameKeyInOrderOneAtATime(t *testing.T) {
	t.Parallel()
	var s keyedSerializer
	var mu sync.Mutex
	var order []int
	running := 0
	overlap := false
	release := make(chan struct{})
	done := make(chan struct{}, 3)
	for i := 1; i <= 3; i++ {
		s.submit("pr:x#1", func() {
			mu.Lock()
			running++
			if running > 1 {
				overlap = true
			}
			order = append(order, i)
			mu.Unlock()
			if i == 1 {
				<-release
			}
			mu.Lock()
			running--
			mu.Unlock()
			done <- struct{}{}
		})
	}
	close(release)
	for range 3 {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("serializer stalled")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap || !slices.Equal(order, []int{1, 2, 3}) {
		t.Fatalf("order %v overlap %v, want [1 2 3] sequential", order, overlap)
	}
}

func TestDispatchKey(t *testing.T) {
	t.Parallel()
	if got := dispatchKey("pull_request", prBody("closed", "Example/Alpha", "x", "y", 7)); got != "pr:example/alpha#7" {
		t.Errorf("pr key = %q", got)
	}
	if got := dispatchKey("push", []byte(`{"ref":"refs/heads/main","repository":{"full_name":"a/b"}}`)); got != "push:a/b@refs/heads/main" {
		t.Errorf("push key = %q", got)
	}
	if got := dispatchKey("installation", []byte(`{}`)); got != "" {
		t.Errorf("installation key = %q, want none", got)
	}
}
