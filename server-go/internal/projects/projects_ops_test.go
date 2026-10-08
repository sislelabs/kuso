package projects

import (
	"context"
	"errors"
	"testing"

	"kuso/server/internal/kube"
)

func TestValidateProjectName(t *testing.T) {
	good := []string{"acme", "my-app", "app123", "a"}
	for _, n := range good {
		if err := validateProjectName(n); err != nil {
			t.Errorf("validateProjectName(%q) = %v, want nil", n, err)
		}
	}
	bad := []string{"Acme", "my_app", "-lead", "trail-", "has space", "2026", "9lives", "way-too-long-project-name-that-exceeds-the-forty-char-budget"}
	for _, n := range bad {
		err := validateProjectName(n)
		if err == nil {
			t.Errorf("validateProjectName(%q) = nil, want ErrInvalid", n)
			continue
		}
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("validateProjectName(%q) err not ErrInvalid: %v", n, err)
		}
	}
}

// Settings could not clear these: description/baseDomain clear on "",
// and defaultRepo had no clear at all (empty fields meant "leave alone").
func TestUpdateProject_ClearsDescriptionBaseDomainDefaultRepo(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		Description: "d",
		BaseDomain:  "example.com",
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/a/b", DefaultBranch: "main"},
	}))
	empty := ""
	out, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{
		Description: &empty, BaseDomain: &empty, ClearDefaultRepo: true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Spec.Description != "" || out.Spec.BaseDomain != "" || out.Spec.DefaultRepo != nil {
		t.Fatalf("not cleared: %+v", out.Spec)
	}
}

func TestUpdateProject_EmptyDefaultRepoClears(t *testing.T) {
	t.Parallel()
	s := fakeService(t, seedProject("alpha", kube.KusoProjectSpec{
		DefaultRepo: &kube.KusoRepoRef{URL: "https://github.com/a/b", DefaultBranch: "main"},
	}))
	out, err := s.Update(context.Background(), "alpha", UpdateProjectRequest{DefaultRepo: &CreateProjectRepoSpec{}})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if out.Spec.DefaultRepo != nil {
		t.Fatalf("defaultRepo {url:\"\"} should clear, got %+v", out.Spec.DefaultRepo)
	}
}
