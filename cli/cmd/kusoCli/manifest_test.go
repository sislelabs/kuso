package kusoCli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `kuso init` writes kuso.yml, but `apply` defaulted to kuso.yaml and
// zero-arg `status` only read kuso.yml — an init'd repo worked with
// status but apply's default missed it (and vice versa for a
// hand-written kuso.yaml). resolveManifestPath is the single shared
// resolver: accept BOTH names, prefer kuso.yml (matching init), note
// when both exist.
func TestResolveManifestPath(t *testing.T) {
	write := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("project: x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("only kuso.yml", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "kuso.yml")
		path, note, err := resolveManifestPath(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(path) != "kuso.yml" {
			t.Errorf("path = %q, want kuso.yml", path)
		}
		if note != "" {
			t.Errorf("unexpected note: %q", note)
		}
	})

	t.Run("only kuso.yaml", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "kuso.yaml")
		path, note, err := resolveManifestPath(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(path) != "kuso.yaml" {
			t.Errorf("path = %q, want kuso.yaml", path)
		}
		if note != "" {
			t.Errorf("unexpected note: %q", note)
		}
	})

	t.Run("both prefer kuso.yml and note the other", func(t *testing.T) {
		dir := t.TempDir()
		write(t, dir, "kuso.yml")
		write(t, dir, "kuso.yaml")
		path, note, err := resolveManifestPath(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(path) != "kuso.yml" {
			t.Errorf("path = %q, want kuso.yml (the name `kuso init` writes)", path)
		}
		if note == "" {
			t.Error("both files exist but no note was returned — the user can't see the ignored kuso.yaml")
		}
		if !strings.Contains(note, "kuso.yaml") {
			t.Errorf("note should name the ignored file, got: %q", note)
		}
	})

	t.Run("neither errors naming both", func(t *testing.T) {
		dir := t.TempDir()
		_, _, err := resolveManifestPath(dir)
		if err == nil {
			t.Fatal("expected an error when no manifest exists")
		}
		if !strings.Contains(err.Error(), "kuso.yml") || !strings.Contains(err.Error(), "kuso.yaml") {
			t.Errorf("error should mention both accepted names, got: %v", err)
		}
	})

	t.Run("directory named kuso.yml is not a manifest", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "kuso.yml"), 0o755); err != nil {
			t.Fatal(err)
		}
		write(t, dir, "kuso.yaml")
		path, _, err := resolveManifestPath(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filepath.Base(path) != "kuso.yaml" {
			t.Errorf("path = %q, want kuso.yaml (kuso.yml is a directory)", path)
		}
	})
}
