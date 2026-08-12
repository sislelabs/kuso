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
		// case-insensitive scheme: git clones HTTPS:// fine, so a
		// case-sensitive matcher was a storable redaction bypass
		{"HTTPS://user:gldt-x@gitlab.com/o/r.git", "HTTPS://gitlab.com/o/r.git"},
		// @ inside the password: strip to the LAST @ before the path,
		// not the first (no `ss@gitlab.com` credential tail)
		{"https://user:p@ss@gitlab.com/o/r.git", "https://gitlab.com/o/r.git"},
		// colon-free userinfo under httpS is GitHub's documented
		// PAT-as-username clone shape — a working credential
		{"https://ghp-FAKEFAKE@github.com/o/r.git", "https://github.com/o/r.git"},
		// …and its schemeless slash-path form
		{"x-access-token-gldt-x@gitlab.com/o/r", "gitlab.com/o/r"},
		// ssh:// userinfo is a plain username the clone NEEDS — kept
		// (stripping produced an unusable URL and leaked nothing)
		{"ssh://git@github.com/o/r.git", "ssh://git@github.com/o/r.git"},
		// …but an ssh password still strips
		{"ssh://git:hunter2@github.com/o/r.git", "ssh://github.com/o/r.git"},
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

// TestRepoURLEchoesRedacted pins round-trip echo detection, including
// the LEGACY redaction form: pre-rewrite releases stripped ssh://
// usernames too, so a client caching that output and saving it back
// must still be treated as an echo, not an intentional userinfo wipe.
func TestRepoURLEchoesRedacted(t *testing.T) {
	t.Parallel()
	cases := []struct {
		stored, incoming string
		want             bool
	}{
		// current redaction output
		{"https://u:tok@gitlab.com/o/r.git", "https://gitlab.com/o/r.git", true},
		// legacy redaction output: ssh username was stripped back then
		{"ssh://git@github.com/o/r.git", "ssh://github.com/o/r.git", true},
		// identical echo → nothing to preserve
		{"ssh://git@github.com/o/r.git", "ssh://git@github.com/o/r.git", false},
		// genuinely different URL → replace, don't preserve
		{"https://u:tok@gitlab.com/o/r.git", "https://gitlab.com/o/OTHER.git", false},
		{"ssh://git@github.com/o/r.git", "https://github.com/o/r.git", false},
		// empty incoming never preserves
		{"https://u:tok@gitlab.com/o/r.git", "", false},
	}
	for _, c := range cases {
		if got := RepoURLEchoesRedacted(c.stored, c.incoming); got != c.want {
			t.Errorf("RepoURLEchoesRedacted(%q, %q) = %v, want %v", c.stored, c.incoming, got, c.want)
		}
	}
}

// Stray whitespace must not defeat the anchored redaction match — a
// pasted " https://user:tok@host/…" is a working clone URL to git.
func TestStripRepoURLCredentials_Whitespace(t *testing.T) {
	t.Parallel()
	in := "  https://user:gldt-x@gitlab.com/o/r.git"
	if got := StripRepoURLCredentials(in); got != "https://gitlab.com/o/r.git" {
		t.Errorf("whitespace-prefixed URL not stripped: %q", got)
	}
	if !RepoURLHasCredentials(in) {
		t.Error("whitespace-prefixed credential URL not detected")
	}
}
