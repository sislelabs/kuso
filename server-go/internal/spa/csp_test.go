package spa

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// LOOP-16: a direct GET of an exported page (/login.html) went through
// http.FileServer and carried no CSP / frame guards. It must get exactly
// the headers the extensionless route gets.
func TestDirectHTMLGetsSameSecurityHeaders(t *testing.T) {
	h, err := Handler(nextExportFS())
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		return rr
	}
	route, direct := get("/login"), get("/login.html")
	if direct.Code != 200 {
		t.Fatalf("/login.html status %d", direct.Code)
	}
	for k := range htmlSecurityHeaders {
		if got, want := direct.Header().Get(k), route.Header().Get(k); got == "" || got != want {
			t.Errorf("%s on /login.html = %q, want %q (same as /login)", k, got, want)
		}
	}
	if js := get("/_next/static/chunks/main.js"); js.Header().Get("Content-Security-Policy") != "" {
		t.Error("static assets should keep their old headers")
	}
}

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
