package releaserun

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The wait-for-addons script exists twice: as this package's Go const (the
// release Job's initContainer) and as a Helm define in the kusoenvironment
// chart (every app pod's initContainer). Helm can't read Go and the operator
// image can't read this repo at render time, so the copy is deliberate — and
// this test is what stops the two drifting apart.
func TestWaitForAddonsScript_ChartMatchesGo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "operator", "helm-charts", "kusoenvironment", "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatalf("read chart helpers: %v", err)
	}
	re := regexp.MustCompile(`(?s)\{\{-\s*define "kusoenvironment\.waitForAddonsScript"\s*-\}\}\n(.*?)\n\{\{-\s*end\s*-\}\}`)
	m := re.FindSubmatch(raw)
	if m == nil {
		t.Fatal("kusoenvironment.waitForAddonsScript define not found in _helpers.tpl")
	}
	chart := strings.TrimSpace(string(m[1]))
	goScript := strings.TrimSpace(waitForAddonsScript)
	if chart != goScript {
		t.Errorf("chart copy of wait-for-addons has drifted from the Go const.\n"+
			"Edit releaserun.waitForAddonsScript, then paste it into the chart define.\n--- go ---\n%s\n--- chart ---\n%s", goScript, chart)
	}
}
