package projects

import (
	"context"
	"testing"
)

// A project name can be reused after a delete. History left behind by the
// old project (archived builds of services deleted before the purge
// existed, or of clones) showed up on the new one as rollback targets.
func TestCreate_DropsLeftoverBuildHistory(t *testing.T) {
	t.Parallel()
	s := fakeService(t)
	var cleaned []string
	s.BuildHistoryCleanupForProject = func(_ context.Context, project string) error {
		cleaned = append(cleaned, project)
		return nil
	}
	if _, err := s.Create(context.Background(), CreateProjectRequest{Name: "alpha"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(cleaned) != 1 || cleaned[0] != "alpha" {
		t.Errorf("project history cleanup calls = %v, want [alpha]", cleaned)
	}
}
