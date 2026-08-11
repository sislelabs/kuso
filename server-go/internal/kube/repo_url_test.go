package kube

import "testing"

func TestStripRepoURLCredentials(t *testing.T) {
	t.Parallel()
	cases := []struct{ in, want string }{
		// deploy-token https form — the sportnopz leak shape (token
		// value is a FAKE placeholder; never commit a real one, GH push
		// protection rejects the push)
		{"https://kuso-deploy:gldt-FAKEFAKEFAKEFAKE@gitlab.com/o/r.git", "https://gitlab.com/o/r.git"},
		// bare (scheme-less) with creds
		{"kuso-deploy:gldt-x@gitlab.com/o/r.git", "gitlab.com/o/r.git"},
		// scheme-ful username-only userinfo still stripped (it precedes
		// a host in URL position; only scp-style is a real username)
		{"ssh://git@github.com/o/r.git", "ssh://github.com/o/r.git"},
		// scp-style SSH — username, not a secret; kept verbatim
		{"git@github.com:o/r.git", "git@github.com:o/r.git"},
		// no creds — unchanged
		{"https://github.com/o/r.git", "https://github.com/o/r.git"},
		{"github.com/o/r", "github.com/o/r"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripRepoURLCredentials(c.in); got != c.want {
			t.Errorf("StripRepoURLCredentials(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !RepoURLHasCredentials("https://a:b@x.com/r") || RepoURLHasCredentials("https://x.com/r") {
		t.Error("RepoURLHasCredentials misclassified")
	}
}
