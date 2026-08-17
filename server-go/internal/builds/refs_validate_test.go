package builds

import "testing"

// Branch names reach the clone init container's /bin/sh script
// (buildcontroller/render.go) where they're single-quoted by
// shellQuote. These cases pin the boundary half of that defense —
// shellQuote's doc comment promises the server validates these fields,
// and for several releases it did not.
func TestValidateGitRef(t *testing.T) {
	valid := []string{
		"main",
		"master",
		"develop",
		"feature/add-login",
		"release/v1.2.3",
		"fix-123",
		"user.name/branch",
		"v1.0.0",
		"deploy/kuso",    // slashed deploy branches are real (see refs slugify)
		"release+hotfix", // + shows up in some release flows
		"a",              // single char
	}
	for _, ref := range valid {
		t.Run("valid/"+ref, func(t *testing.T) {
			if err := ValidateGitRef(ref); err != nil {
				t.Errorf("ValidateGitRef(%q) = %v, want nil", ref, err)
			}
		})
	}

	invalid := []struct {
		name string
		ref  string
	}{
		{"empty", ""},
		// Shell breakout attempts — the payloads that matter.
		{"command substitution", "main$(id)"},
		{"backtick substitution", "main`id`"},
		{"semicolon chain", "main; rm -rf /"},
		{"pipe", "main|id"},
		{"ampersand", "main&id"},
		{"single quote breakout", `main'; id; echo '`},
		{"double quote", `main"x`},
		{"newline", "main\nrm -rf /"},
		{"space", "my branch"},
		{"backslash", `main\x`},
		{"dollar var", "main$HOME"},
		{"redirect", "main>out"},

		// git's own ref rules.
		{"double dot", "main..evil"},
		{"double slash", "feature//x"},
		{"leading slash", "/main"},
		{"trailing slash", "main/"},
		{"reflog syntax", "main@{1}"},
		{"lock suffix", "main.lock"},
		{"trailing dot", "main."},
		{"leading dash is not a letter or digit", "-main"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if err := ValidateGitRef(tc.ref); err == nil {
				t.Errorf("ValidateGitRef(%q) = nil, want error", tc.ref)
			}
		})
	}
}

func TestValidateRepoURL(t *testing.T) {
	valid := []string{
		"https://github.com/org/repo",
		"https://github.com/org/repo.git",
		"http://gitea.internal/org/repo.git",
		"git@github.com:org/repo.git",
		"git@gitea.internal:team/sub-repo",
	}
	for _, u := range valid {
		t.Run("valid/"+u, func(t *testing.T) {
			if err := ValidateRepoURL(u); err != nil {
				t.Errorf("ValidateRepoURL(%q) = %v, want nil", u, err)
			}
		})
	}

	invalid := []struct {
		name string
		url  string
	}{
		{"empty", ""},
		{"command substitution", "https://github.com/org/repo$(id)"},
		{"backtick", "https://github.com/org/`id`"},
		{"semicolon", "https://github.com/org/repo; id"},
		{"space", "https://github.com/org/ repo"},
		{"newline", "https://github.com/org/repo\nid"},
		{"single quote", `https://github.com/o'rg/repo`},
		{"pipe", "https://github.com/org/repo|id"},
		// file:// would let a build read the builder's own filesystem.
		{"file scheme", "file:///etc/shadow"},
		{"ssh scheme unsupported", "ssh://git@github.com/org/repo"},
		{"bare path", "/etc/passwd"},
		{"no scheme", "github.com/org/repo"},
		{"scheme only", "https://"},
	}
	for _, tc := range invalid {
		t.Run("invalid/"+tc.name, func(t *testing.T) {
			if err := ValidateRepoURL(tc.url); err == nil {
				t.Errorf("ValidateRepoURL(%q) = nil, want error", tc.url)
			}
		})
	}
}
