package addons

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestDefaultAddonVersion(t *testing.T) {
	if got := DefaultAddonVersion("postgres", false); got != "18" {
		t.Errorf("postgres = %q", got)
	}
	if got := DefaultAddonVersion("postgres", true); got != newHAPostgresVersion {
		t.Errorf("postgres ha = %q", got)
	}
	if got := DefaultAddonVersion("memcached", false); got != "" {
		t.Errorf("unknown kind = %q, want empty", got)
	}
}

// Every kind the chart renders a versioned image for must have a
// stamped default, or new addons of that kind silently fall back to the
// chart's old default.
func TestEveryVersionedChartKindHasADefault(t *testing.T) {
	dir := filepath.Join("..", "..", "..", "operator", "helm-charts", "kusoaddon", "templates")
	files, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("glob templates: %v", err)
	}
	kindRe := regexp.MustCompile(`eq \.Values\.kind "([a-z0-9]+)"`)
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		if !regexp.MustCompile(`\$version := \.Values\.version \| default`).Match(raw) {
			continue
		}
		m := kindRe.FindSubmatch(raw)
		if m == nil {
			t.Errorf("%s: versioned template without a kind guard", filepath.Base(f))
			continue
		}
		if DefaultAddonVersion(string(m[1]), false) == "" {
			t.Errorf("kind %q (%s) has no stamped default version", m[1], filepath.Base(f))
		}
	}
}
