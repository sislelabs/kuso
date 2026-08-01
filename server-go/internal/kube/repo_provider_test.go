package kube

import "testing"

func TestRepoProviderForURL(t *testing.T) {
	t.Parallel()
	cases := map[string]RepoProvider{
		"https://github.com/acme/app.git":       ProviderGitHub,
		"https://github.com/acme/app":           ProviderGitHub,
		"git@github.com:acme/app.git":           ProviderGitHub,
		"https://gitlab.com/acme/app.git":       ProviderGitLab,
		"git@gitlab.com:acme/app.git":           ProviderGitLab,
		"https://gitlab.acme.com/team/app.git":  ProviderGitLab, // self-hosted
		"gitlab.example.org/team/app":           ProviderGitLab, // no scheme
		"https://code.acme.com/team/app.git":    ProviderGitHub, // unknown → default github (preserves today)
		"":                                      ProviderGitHub,
	}
	for url, want := range cases {
		if got := RepoProviderForURL(url); got != want {
			t.Errorf("RepoProviderForURL(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestRepoProviderForRef(t *testing.T) {
	t.Parallel()
	// Explicit provider wins over URL inference.
	if got := RepoProviderForRef(&KusoRepoRef{URL: "https://github.com/a/b", Provider: "gitlab"}); got != ProviderGitLab {
		t.Errorf("explicit provider should win, got %q", got)
	}
	// A bogus explicit provider falls back to URL inference.
	if got := RepoProviderForRef(&KusoRepoRef{URL: "https://gitlab.com/a/b", Provider: "svn"}); got != ProviderGitLab {
		t.Errorf("bogus provider should fall back to URL, got %q", got)
	}
	// Empty provider → inference.
	if got := RepoProviderForRef(&KusoRepoRef{URL: "https://gitlab.com/a/b"}); got != ProviderGitLab {
		t.Errorf("inference failed, got %q", got)
	}
	// nil ref → github default.
	if got := RepoProviderForRef(nil); got != ProviderGitHub {
		t.Errorf("nil ref should default github, got %q", got)
	}
}
