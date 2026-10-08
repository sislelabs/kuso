package builds

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRemote struct {
	sha    string
	err    error
	url    string
	branch string
	token  string
}

func (f *fakeRemote) HeadSHA(_ context.Context, repoURL, branch, token string) (string, error) {
	f.url, f.branch, f.token = repoURL, branch, token
	return f.sha, f.err
}

// With no GitHub App at all, a manual build still records the commit it
// ships: the branch head is read straight from the git remote.
func TestCreate_ManualTrigger_NoApp_ResolvesBranchHeadFromRemote(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 0),
		seedService("e2e", "api"),
	)
	remote := &fakeRemote{sha: headSHA}
	s.Remote = remote

	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "staging"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Spec.Ref != headSHA {
		t.Errorf("spec.ref = %q, want %q", got.Spec.Ref, headSHA)
	}
	if got.Spec.Image == nil || got.Spec.Image.Tag != headSHA[:12] {
		t.Errorf("image tag = %+v, want %q", got.Spec.Image, headSHA[:12])
	}
	if remote.branch != "staging" || !strings.HasSuffix(remote.url, "/api") {
		t.Errorf("remote asked for %s@%s", remote.url, remote.branch)
	}
}

func TestCreate_ManualTrigger_NoApp_RemoteFailureFallsBackToSynthetic(t *testing.T) {
	t.Parallel()
	s := fakeService(t,
		seedProject("e2e", "main", "https://github.com/ivo9999/api", 0),
		seedService("e2e", "api"),
	)
	s.Remote = &fakeRemote{err: errors.New("unreachable")}

	got, err := s.Create(context.Background(), "e2e", "api", CreateBuildRequest{Branch: "staging"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !strings.HasPrefix(got.Spec.Ref, "staging-") || shaRE.MatchString(got.Spec.Ref) {
		t.Errorf("spec.ref = %q, want synthetic staging-<ms>", got.Spec.Ref)
	}
}
