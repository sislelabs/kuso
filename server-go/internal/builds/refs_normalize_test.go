package builds

import "testing"

func TestNormalizeRepoURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"git@github.com:acme/api.git":      "https://github.com/acme/api.git",
		"git@gitlab.com:group/sub/repo":    "https://gitlab.com/group/sub/repo",
		"https://github.com/acme/api.git":  "https://github.com/acme/api.git",
		"http://git.example.com/acme/api":  "http://git.example.com/acme/api",
		"https://user@github.com/acme/api": "https://user@github.com/acme/api",
		"":                                 "",
	}
	for in, want := range cases {
		if got := NormalizeRepoURL(in); got != want {
			t.Errorf("NormalizeRepoURL(%q) = %q, want %q", in, got, want)
		}
	}
}
