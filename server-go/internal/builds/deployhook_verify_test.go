package builds

import (
	"context"
	"errors"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

type offBranchChecker struct{}

func (offBranchChecker) CheckRepoAccess(context.Context, int64, string, string) error { return nil }
func (offBranchChecker) CommitOnBranch(context.Context, int64, string, string, string, string) (bool, error) {
	return false, nil
}

// LINT-2: a failed project read skipped the commit-on-branch check, so a
// leaked hook URL could deploy an off-branch commit during a kube blip.
func TestVerifyHookRefFailsClosedOnProjectReadError(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("shop", "main", "https://github.com/example/web", 7), seedService("shop", "web"))
	s.RepoAccess = offBranchChecker{}
	ctx := context.Background()
	if err := s.VerifyHookRef(ctx, "shop", "web", []string{"main"}, "abc123"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("off-branch commit with a readable project: %v, want ErrInvalid", err)
	}
	s.Kube.Dynamic.(*dynamicfake.FakeDynamicClient).PrependReactor("get", "kusoprojects",
		func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("apiserver unavailable")
		})
	if err := s.VerifyHookRef(ctx, "shop", "web", []string{"main"}, "abc123"); err == nil {
		t.Fatal("project read error let the hook through unchecked")
	}
}
