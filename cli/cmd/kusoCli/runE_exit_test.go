package kusoCli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apply, init and doctor used to os.Exit(1) inside Run, which skipped
// deferred cleanup and killed any test that drove them. They now return
// errors; before the fix these calls would have terminated the test binary.
func TestInitReturnsErrorInsteadOfExiting(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Cleanup(func() { initRuntime = ""; initTemplate = "" })
	_, err := runRoot(t, "init", "--runtime", "bogus")
	if err == nil || !strings.Contains(err.Error(), `unknown --runtime "bogus"`) {
		t.Fatalf("want unknown-runtime error, got %v", err)
	}
	initRuntime = ""
	_, err = runRoot(t, "init", "--template", "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown template "nope"`) {
		t.Fatalf("want unknown-template error, got %v", err)
	}
}

func TestApplyReturnsErrorInsteadOfExiting(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "kuso.yml")
	if err := os.WriteFile(f, []byte("services: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := runRoot(t, "apply", f)
	if err == nil || !strings.Contains(err.Error(), "project:") {
		t.Fatalf("want missing-project error, got %v", err)
	}
	_, err = runRoot(t, "apply", filepath.Join(dir, "missing.yml"))
	if err == nil || !strings.Contains(err.Error(), "missing.yml") {
		t.Fatalf("want read error, got %v", err)
	}
}
