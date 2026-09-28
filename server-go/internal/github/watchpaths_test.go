package github

import (
	"fmt"
	"strings"
	"testing"

	"kuso/server/internal/kube"
)

func TestMatchWatchGlob(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern, file string
		want          bool
	}{
		{"apps/web/**", "apps/web/src/page.tsx", true},
		{"apps/web/**", "apps/web/package.json", true},
		{"apps/web/**", "apps/api/main.go", false},
		{"apps/web/**", "apps/webhooks/x.go", false},
		{"**/*.go", "main.go", true},
		{"**/*.go", "internal/a/b.go", true},
		{"**/*.go", "internal/a/b.ts", false},
		{"packages/*/src/**", "packages/ui/src/button.tsx", true},
		{"packages/*/src/**", "packages/ui/test/button.tsx", false},
		{"pnpm-lock.yaml", "pnpm-lock.yaml", true},
		{"pnpm-lock.yaml", "apps/web/pnpm-lock.yaml", false},
		// A bare directory (no glob chars) matches everything under it.
		{"apps/web", "apps/web/src/a.ts", true},
		{"apps/web/", "apps/web/src/a.ts", true},
		{"./apps/web/**", "apps/web/src/a.ts", true},
		{"/apps/web/**", "apps/web/src/a.ts", true},
		{"**", "anything/at/all", true},
		{"apps/**/Dockerfile", "apps/web/Dockerfile", true},
		{"apps/**/Dockerfile", "apps/Dockerfile", true},
	}
	for _, c := range cases {
		if got := matchWatchGlob(c.pattern, c.file); got != c.want {
			t.Errorf("matchWatchGlob(%q, %q) = %v, want %v", c.pattern, c.file, got, c.want)
		}
	}
}

func TestEffectiveWatchPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		spec kube.KusoServiceSpec
		want []string
	}{
		{"explicit wins over path", kube.KusoServiceSpec{WatchPaths: []string{"packages/**"}, Repo: &kube.KusoRepoRef{Path: "apps/web"}}, []string{"packages/**"}},
		// Opt-in only: defaulting to <path>/** would stop existing monorepo
		// services rebuilding when shared code (packages/, the lockfile)
		// changes, with no warning on upgrade.
		{"monorepo path without watch paths watches everything", kube.KusoServiceSpec{Repo: &kube.KusoRepoRef{Path: "apps/web"}}, nil},
		{"root path watches everything", kube.KusoServiceSpec{Repo: &kube.KusoRepoRef{Path: "."}}, nil},
		{"no repo watches everything", kube.KusoServiceSpec{}, nil},
		{"blank entries ignored", kube.KusoServiceSpec{WatchPaths: []string{" ", ""}, Repo: &kube.KusoRepoRef{Path: "apps/api"}}, nil},
	}
	for _, c := range cases {
		got := effectiveWatchPaths(&c.spec)
		if fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParsePushChanges(t *testing.T) {
	t.Parallel()
	body := []byte(`{"commits":[
		{"added":["apps/web/a.ts"],"modified":["README.md"],"removed":[]},
		{"added":[],"modified":[],"removed":["apps/old/x.go"]}
	]}`)
	ch := parsePushChanges(body)
	if !ch.Known {
		t.Fatalf("expected known file list, got unknown (%s)", ch.Reason)
	}
	want := "README.md,apps/old/x.go,apps/web/a.ts"
	if got := strings.Join(ch.Files, ","); got != want {
		t.Errorf("files = %q, want %q", got, want)
	}

	unknown := map[string]string{
		"no commits":          `{"commits":[]}`,
		"commits missing":     `{}`,
		"empty commit only":   `{"commits":[{"added":[],"modified":[],"removed":[]}]}`,
		"forced push":         `{"forced":true,"commits":[{"modified":["a"]}]}`,
		"branch created":      `{"created":true,"commits":[{"modified":["a"]}]}`,
		"commit cap reached":  fmt.Sprintf(`{"commits":[%s]}`, strings.TrimSuffix(strings.Repeat(`{"modified":["a"]},`, pushCommitCap), ",")),
		"file cap reached":    fmt.Sprintf(`{"commits":[{"modified":[%s]}]}`, strings.TrimSuffix(strings.Repeat(`"f",`, pushFileCap), ",")),
		"undecodable commits": `{"commits":"nope"}`,
	}
	for name, b := range unknown {
		if ch := parsePushChanges([]byte(b)); ch.Known {
			t.Errorf("%s: expected unknown (build anyway), got known %v", name, ch.Files)
		}
	}
}

func TestSkipCIDirective(t *testing.T) {
	t.Parallel()
	skip := []string{
		"fix typo [skip ci]",
		"[ci skip] docs",
		"chore: bump\n\n[skip kuso]",
		"docs [SKIP CI]",
	}
	for _, m := range skip {
		if _, ok := skipCIDirective(m); !ok {
			t.Errorf("%q should skip", m)
		}
	}
	keep := []string{"", "skip ci without brackets", "[skip-ci]", "feat: add ci skip toggle"}
	for _, m := range keep {
		if d, ok := skipCIDirective(m); ok {
			t.Errorf("%q should not skip (matched %q)", m, d)
		}
	}
}

func TestPushTouchesService(t *testing.T) {
	t.Parallel()
	known := pushChanges{Known: true, Files: []string{"apps/api/main.go", "README.md"}}
	cases := []struct {
		name     string
		patterns []string
		ch       pushChanges
		want     bool
	}{
		{"no patterns = everything", nil, known, true},
		{"matching pattern", []string{"apps/api/**"}, known, true},
		{"second pattern matches", []string{"apps/web/**", "README.md"}, known, true},
		{"no pattern matches", []string{"apps/web/**", "packages/**"}, known, false},
		{"unknown files build anyway", []string{"apps/web/**"}, pushChanges{Known: false, Reason: "truncated"}, true},
	}
	for _, c := range cases {
		got, reason := pushTouchesService(c.patterns, c.ch)
		if got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, reason, c.want)
		}
		if !got && reason == "" {
			t.Errorf("%s: a skip must carry a reason", c.name)
		}
	}
}
