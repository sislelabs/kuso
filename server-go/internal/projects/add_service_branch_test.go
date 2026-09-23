package projects

import (
	"context"
	"testing"

	"kuso/server/internal/kube"
)

// The web picks the repo's own default branch (e.g. master) and sends it
// as repo.defaultBranch. It used to be dropped, so production tracked the
// project default (main) while the UI said "building from master".
func TestAddService_RepoDefaultBranchWinsOverProjectDefault(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/x/y", DefaultBranch: "main"},
		BaseDomain:  "alpha.example.com",
	}))

	created, err := s.AddService(context.Background(), "alpha", CreateServiceRequest{
		Name:    "web",
		Runtime: "dockerfile",
		Repo:    &CreateServiceRepo{URL: "https://github.com/x/legacy", DefaultBranch: "master"},
	})
	if err != nil {
		t.Fatalf("AddService: %v", err)
	}
	if created.Spec.Repo == nil || created.Spec.Repo.DefaultBranch != "master" {
		t.Errorf("service repo.defaultBranch = %+v, want master", created.Spec.Repo)
	}
	env, err := s.GetEnvironment(context.Background(), "alpha", "alpha-web-production")
	if err != nil {
		t.Fatalf("production env: %v", err)
	}
	if env.Spec.Branch != "master" {
		t.Errorf("production env branch = %q, want master", env.Spec.Branch)
	}
}
