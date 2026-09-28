package releaserun

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The wait-for-addons script exists as this package's Go const (the release
// Job's initContainer) and as a Helm define in every chart whose pods start
// against project addons: kusoenvironment (app pods), kusorun (`kuso run`
// Jobs) and kusocron (cron Jobs). Helm can't read Go and the operator image
// can't read this repo at render time, so the copies are deliberate — and
// this test is what stops them drifting apart.
func TestWaitForAddonsScript_ChartMatchesGo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	goScript := strings.TrimSpace(waitForAddonsScript)
	for _, chart := range []string{"kusoenvironment", "kusorun", "kusocron"} {
		t.Run(chart, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, "operator", "helm-charts", chart, "templates", "_helpers.tpl"))
			if err != nil {
				t.Fatalf("read chart helpers: %v", err)
			}
			re := regexp.MustCompile(`(?s)\{\{-\s*define "` + chart + `\.waitForAddonsScript"\s*-\}\}\n(.*?)\n\{\{-\s*end\s*-\}\}`)
			m := re.FindSubmatch(raw)
			if m == nil {
				t.Fatalf("%s.waitForAddonsScript define not found in _helpers.tpl", chart)
			}
			got := strings.TrimSpace(string(m[1]))
			if got != goScript {
				t.Errorf("%s chart copy of wait-for-addons has drifted from the Go const.\n"+
					"Edit releaserun.waitForAddonsScript, then paste it into the chart define.\n--- go ---\n%s\n--- chart ---\n%s", chart, goScript, got)
			}
		})
	}
}

// The chart inits must run the same image as the release Job's init.
func TestWaitForAddonsImage_ChartsMatchGo(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	want := "image: " + WaitForAddonsInitContainer(nil, nil).Image
	files := map[string]string{
		"kusoenvironment": "deployment.yaml",
		"kusorun":         "job.yaml",
		"kusocron":        "cronjob.yaml",
	}
	for chart, file := range files {
		raw, err := os.ReadFile(filepath.Join(root, "operator", "helm-charts", chart, "templates", file))
		if err != nil {
			t.Fatalf("read %s/%s: %v", chart, file, err)
		}
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s/%s: wait-for-addons init should use %q (the release Job's image)", chart, file, want)
		}
	}
}
