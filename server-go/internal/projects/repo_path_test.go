package projects

import (
	"context"
	"errors"
	"testing"
)

// Finding 37 (domain side): defaultRepo.path must survive project
// create AND update — the conversion used to copy only URL + branch.
func TestProjectCreateUpdate_PersistDefaultRepoPath(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	ctx := context.Background()

	created, err := s.Create(ctx, CreateProjectRequest{
		Name: "mono",
		DefaultRepo: &CreateProjectRepoSpec{
			URL:  "https://github.com/x/mono.git",
			Path: "services/api",
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.Spec.DefaultRepo == nil || created.Spec.DefaultRepo.Path != "services/api" {
		t.Fatalf("create dropped defaultRepo.path: %+v", created.Spec.DefaultRepo)
	}

	updated, err := s.Update(ctx, "mono", UpdateProjectRequest{
		DefaultRepo: &CreateProjectRepoSpec{Path: "services/web"},
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Spec.DefaultRepo == nil || updated.Spec.DefaultRepo.Path != "services/web" {
		t.Fatalf("update dropped defaultRepo.path: %+v", updated.Spec.DefaultRepo)
	}
	// URL untouched by the path-only patch.
	if updated.Spec.DefaultRepo.URL != "https://github.com/x/mono.git" {
		t.Errorf("update clobbered URL: %q", updated.Spec.DefaultRepo.URL)
	}
}

// The path flows into shell contexts downstream (same as the
// service-level repo.path), so it gets the same validator.
func TestProjectCreate_RejectsMaliciousDefaultRepoPath(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	_, err := s.Create(context.Background(), CreateProjectRequest{
		Name: "mono",
		DefaultRepo: &CreateProjectRepoSpec{
			URL:  "https://github.com/x/mono.git",
			Path: `."; rm -rf / #`,
		},
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("want ErrInvalid for shell-breakout path, got %v", err)
	}
}
