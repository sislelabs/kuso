package spa

import (
	"strings"
	"testing"
)

// The one-click GitHub App setup (settings/github) submits an HTML form
// straight to github.com. A form-action that omits it makes the browser
// drop the submit silently: the button sits on "Redirecting to GitHub…".
func TestCSP_FormActionAllowsGitHubManifestPost(t *testing.T) {
	var sources []string
	for _, d := range strings.Split(htmlSecurityHeaders["Content-Security-Policy"], ";") {
		f := strings.Fields(d)
		if len(f) > 0 && f[0] == "form-action" {
			sources = f[1:]
		}
	}
	for _, want := range []string{"'self'", "https://github.com"} {
		found := false
		for _, s := range sources {
			if s == want {
				found = true
			}
		}
		if !found {
			t.Errorf("form-action %v is missing %s", sources, want)
		}
	}
}
