package github

import (
	"context"
	"testing"

	"kuso/server/internal/spec"
)

func TestApplyConfigFromRepo_SkipsWhenNoFile(t *testing.T) {
	// fetch returns (nil, false, nil) for every path → not found.
	called := false
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return nil, false, nil
	}
	apply := func(ctx context.Context, _ *spec.File) error { called = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err != nil {
		t.Fatalf("missing file must not error: %v", err)
	}
	if called {
		t.Fatal("apply must not run when kuso.yaml is absent")
	}
}

func TestApplyConfigFromRepo_RejectsProjectMismatch(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return []byte("project: other\n"), true, nil
	}
	applied := false
	apply := func(ctx context.Context, _ *spec.File) error { applied = true; return nil }
	err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj")
	if err == nil {
		t.Fatal("project mismatch must return an error")
	}
	if applied {
		t.Fatal("apply must not run on project mismatch")
	}
}

func TestApplyConfigFromRepo_AppliesMatchingFile(t *testing.T) {
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		return []byte("project: proj\n"), true, nil
	}
	var got string
	apply := func(ctx context.Context, f *spec.File) error { got = f.Project; return nil }
	if err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got != "proj" {
		t.Fatalf("apply was not called with the parsed file: got %q", got)
	}
}

func TestApplyConfigFromRepo_FallsBackToKusoYml(t *testing.T) {
	// kuso.yaml absent, kuso.yml present → fetch is tried for both
	// paths and the .yml content is applied.
	var paths []string
	fetch := func(ctx context.Context, owner, repo, ref, path string) ([]byte, bool, error) {
		paths = append(paths, path)
		if path == "kuso.yml" {
			return []byte("project: proj\n"), true, nil
		}
		return nil, false, nil
	}
	applied := false
	apply := func(ctx context.Context, _ *spec.File) error { applied = true; return nil }
	if err := applyConfigFromRepo(context.Background(), fetch, apply, "o", "r", "sha", "proj"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !applied {
		t.Fatal("apply must run when kuso.yml is present")
	}
	if len(paths) != 2 || paths[0] != "kuso.yaml" || paths[1] != "kuso.yml" {
		t.Fatalf("expected fetch to try kuso.yaml then kuso.yml, got %v", paths)
	}
}
