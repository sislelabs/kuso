package builds

import (
	"context"
	"testing"
)

type offBranchChecker struct{}

func (offBranchChecker) CheckRepoAccess(context.Context, int64, string, string) error { return nil }
func (offBranchChecker) CommitOnBranch(context.Context, int64, string, string, string, string) (bool, error) {
	return false, nil
}

// LINT-2: a failed project read skipped the commit-on-branch check, so a
// leaked hook URL could deploy an off-branch commit during a kube blip.
func TestVerifyHookRef_FailsClosedWhenProjectUnreadable(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedService("alpha", "web"))
	s.RepoAccess = offBranchChecker{}
	if err := s.VerifyHookRef(context.Background(), "alpha", "web", []string{"main"}, "deadbeef"); err == nil {
		t.Fatal("VerifyHookRef passed an unverified commit when the project could not be read")
	}
}
