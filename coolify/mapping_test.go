package coolify

import (
	"strings"
	"testing"
)

func TestSlugifyName(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "x-unnamed"},
		{"  ", "x-unnamed"},
		{"!!!", "x-unnamed"},
		{"My Project", "my-project"},
		{"FooBar/123", "foobar-123"},
		{"  trim-me  ", "trim-me"},
		{"already-fine", "already-fine"},
		{"___underscores___", "underscores"},
		{strings.Repeat("a", 80), strings.Repeat("a", 50)},
		// Trailing dash after truncation is stripped.
		{strings.Repeat("a", 49) + "!" + strings.Repeat("b", 30), strings.Repeat("a", 49)},
	}
	for _, c := range cases {
		got := SlugifyName(c.in)
		if got != c.want {
			t.Errorf("SlugifyName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestAssignKusoSlugs(t *testing.T) {
	names := []string{"Foo", "foo", "Foo!"}
	out := AssignKusoSlugs(names)
	// All three slugify to "foo"; first wins the bare slug, the
	// rest get numeric suffixes (-2, -3) in source order.
	if out["Foo"] != "foo" {
		t.Errorf("first Foo: got %q, want foo", out["Foo"])
	}
	if out["foo"] != "foo-2" {
		t.Errorf("second foo: got %q, want foo-2", out["foo"])
	}
	if out["Foo!"] != "foo-3" {
		t.Errorf("third Foo!: got %q, want foo-3", out["Foo!"])
	}
}

func TestNormalizeRepoURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"owner/repo", "https://github.com/owner/repo"},
		{"owner/repo.git", "https://github.com/owner/repo"},
		{"https://github.com/owner/repo", "https://github.com/owner/repo"},
		{"https://github.com/owner/repo.git", "https://github.com/owner/repo"},
		{"http://gitea.example.com/owner/repo", "http://gitea.example.com/owner/repo"},
		// SSH-style left alone (kuso won't build from it but we
		// don't want to double-prefix something that already has
		// a host).
		{"git@github.com:owner/repo.git", "git@github.com:owner/repo"},
	}
	for _, c := range cases {
		got := NormalizeRepoURL(c.in)
		if got != c.want {
			t.Errorf("NormalizeRepoURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSelectEnvVars(t *testing.T) {
	in := []EnvVar{
		{Key: "NODE_VERSION", Value: "22", IsPreview: false},
		{Key: "NODE_VERSION", Value: "22", IsPreview: true},  // preview dup → dropped
		{Key: "PORT", Value: "3000", IsCoolify: true},        // coolify-managed → dropped
		{Key: "API_URL", RealValue: "https://api", Value: "{{X}}"}, // real_value wins
		{Key: "", Value: "x"},                                 // empty key → dropped
		{Key: "DUP", Value: "first"},
		{Key: "DUP", Value: "second"},                         // last-wins
	}
	out := SelectEnvVars(in)
	got := map[string]string{}
	for _, e := range out {
		got[e.Key] = e.Value
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 vars (NODE_VERSION, API_URL, DUP), got %d: %+v", len(out), out)
	}
	if got["NODE_VERSION"] != "22" {
		t.Errorf("NODE_VERSION = %q, want 22 (preview dup dropped)", got["NODE_VERSION"])
	}
	if got["API_URL"] != "https://api" {
		t.Errorf("API_URL = %q, want real_value", got["API_URL"])
	}
	if got["DUP"] != "second" {
		t.Errorf("DUP = %q, want last-wins 'second'", got["DUP"])
	}
	if _, ok := got["PORT"]; ok {
		t.Error("PORT (is_coolify) must be dropped")
	}
}

func TestNormalizeBaseDir(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"  ", ""},
		{"/", ""},        // Coolify repo-root → kuso empty path
		{"/apps/web", "apps/web"},
		{"apps/web", "apps/web"},
		{"/apps/web/", "apps/web"},
		{"/src", "src"},
	}
	for _, c := range cases {
		got := NormalizeBaseDir(c.in)
		if got != c.want {
			t.Errorf("NormalizeBaseDir(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRuntimeForBuildPack(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "dockerfile"},
		{"dockerfile", "dockerfile"},
		{"DockerFile", "dockerfile"},
		{"nixpacks", "nixpacks"},
		{"Nixpacks", "nixpacks"},
		{"static", "static"},
		{"buildpacks", "dockerfile"}, // unknown → dockerfile
		{"  ", "dockerfile"},
	}
	for _, c := range cases {
		got := RuntimeForBuildPack(c.in)
		if got != c.want {
			t.Errorf("RuntimeForBuildPack(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestParseFirstPort(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"  ", 0},
		{"3000", 3000},
		{"3000,8080", 3000},
		{"3000:3000", 3000},
		{"3000:80", 3000},
		{"abc,3000", 3000},
		{"0", 0},
		{"99999", 0}, // > 65535 rejected
		{"-1", 0},
	}
	for _, c := range cases {
		got := ParseFirstPort(c.in)
		if got != c.want {
			t.Errorf("ParseFirstPort(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestServiceSlugFromApp(t *testing.T) {
	cases := []struct {
		label, appName, repo, want string
	}{
		{"falls back to name", "my-app", "", "my-app"},
		{"repo basename", "ignored", "biznesguys/todo-api", "todo-api"},
		{"strips .git", "ignored", "owner/repo.git", "repo"},
		{"strips :branch suffix", "ignored", "owner/repo:main-abc123", "repo"},
		{"full URL", "ignored", "https://github.com/owner/repo.git", "repo"},
		{"empty repo + empty name", "", "", "x-unnamed"},
	}
	for _, c := range cases {
		a := &Application{Name: c.appName, GitRepository: c.repo}
		if got := ServiceSlugFromApp(a); got != c.want {
			t.Errorf("%s: ServiceSlugFromApp(%+v) = %q, want %q", c.label, a, got, c.want)
		}
	}
	if got := ServiceSlugFromApp(nil); got != "x-unnamed" {
		t.Errorf("nil app: got %q, want x-unnamed", got)
	}
}

func TestItemUUIDAndKind(t *testing.T) {
	cases := []struct {
		name string
		it   Item
		uuid string
		kind string
	}{
		{"app", Item{App: &Application{UUID: "u-app"}}, "u-app", "application"},
		{"db", Item{Database: &Database{UUID: "u-db"}}, "u-db", "database"},
		{"svc", Item{Service: &Service{UUID: "u-svc"}}, "u-svc", "service"},
		{"empty", Item{}, "", "unknown"},
	}
	for _, c := range cases {
		if got := ItemUUID(c.it); got != c.uuid {
			t.Errorf("%s: ItemUUID got %q, want %q", c.name, got, c.uuid)
		}
		if got := ItemKind(c.it); got != c.kind {
			t.Errorf("%s: ItemKind got %q, want %q", c.name, got, c.kind)
		}
	}
}
