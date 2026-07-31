package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// resetServiceSetRepoFlags clears the package-level flag state the repo
// path of `service set` reads, so one test's flags don't bleed into the
// next. The flags are bound as package globals (cobra style here), and
// each test only exercises a subset.
func resetServiceSetRepoFlags(t *testing.T) {
	t.Helper()
	serviceSetRepo = ""
	serviceSetProvider = ""
	serviceSetGitlabToken = ""
	serviceSetGitlabTokenStdin = false
	serviceSetPath = ""
	serviceSetBranch = ""
	// Reset cobra's "Changed" bookkeeping on every repo-related flag.
	for _, name := range []string{"repo", "provider", "gitlab-token", "gitlab-token-stdin", "path", "branch"} {
		if f := serviceSetCmd.Flags().Lookup(name); f != nil {
			f.Changed = false
		}
	}
}

// stubServiceServer returns an httptest server that answers the GET
// current-spec probe with the given repo JSON and records the PATCH body.
func stubServiceServer(t *testing.T, currentRepoJSON string, gotPatch *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"spec":{"repo":`+currentRepoJSON+`}}`)
		case http.MethodPatch:
			defer r.Body.Close()
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			*gotPatch = body
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "{}")
		default:
			http.Error(w, "unexpected method "+r.Method, 405)
		}
	}))
}

// repoBlock pulls the repo sub-object out of a decoded PATCH body.
func repoBlock(t *testing.T, patch map[string]any) map[string]any {
	t.Helper()
	repo, ok := patch["repo"].(map[string]any)
	if !ok {
		t.Fatalf("PATCH body missing repo block: %+v", patch)
	}
	return repo
}

// TestServiceSet_GitlabRepoWithToken drives `service set --repo --provider
// --gitlab-token` and asserts the PATCH body carries url + provider + token.
func TestServiceSet_GitlabRepoWithToken(t *testing.T) {
	var patch map[string]any
	srv := stubServiceServer(t, `{"url":"https://github.com/old/repo.git","defaultBranch":"main","path":"."}`, &patch)
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	resetServiceSetRepoFlags(t)
	defer resetServiceSetRepoFlags(t)
	mustSet(t, serviceSetCmd, "repo", "https://gitlab.com/acme/api.git")
	mustSet(t, serviceSetCmd, "provider", "gitlab")
	mustSet(t, serviceSetCmd, "gitlab-token", "glpat-secret-value")

	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}); err != nil {
		t.Fatalf("service set RunE: %v", err)
	}

	repo := repoBlock(t, patch)
	if got := repo["url"]; got != "https://gitlab.com/acme/api.git" {
		t.Errorf("repo.url = %v, want the new gitlab URL", got)
	}
	if got := repo["provider"]; got != "gitlab" {
		t.Errorf("repo.provider = %v, want gitlab", got)
	}
	if got := repo["token"]; got != "glpat-secret-value" {
		t.Errorf("repo.token = %v, want the supplied token", got)
	}
}

// TestServiceSet_ProviderNormalisedAndValidated checks provider is
// lower-cased and rejected when not github|gitlab.
func TestServiceSet_ProviderNormalisedAndValidated(t *testing.T) {
	var patch map[string]any
	srv := stubServiceServer(t, `{"url":"https://gitlab.com/acme/api.git","defaultBranch":"main","path":"."}`, &patch)
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	// Uppercase provider is normalised to lowercase.
	resetServiceSetRepoFlags(t)
	mustSet(t, serviceSetCmd, "provider", "GitLab")
	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}); err != nil {
		t.Fatalf("service set RunE: %v", err)
	}
	if got := repoBlock(t, patch)["provider"]; got != "gitlab" {
		t.Errorf("provider not normalised: got %v", got)
	}

	// Bogus provider is rejected before any PATCH is sent.
	resetServiceSetRepoFlags(t)
	defer resetServiceSetRepoFlags(t)
	mustSet(t, serviceSetCmd, "provider", "bitbucket")
	err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"})
	if err == nil || !strings.Contains(err.Error(), "must be github|gitlab") {
		t.Fatalf("expected provider validation error, got %v", err)
	}
}

// TestServiceSet_TokenNeverEchoed guards that the supplied token does not
// appear in anything the command writes to stdout.
func TestServiceSet_TokenNeverEchoed(t *testing.T) {
	var patch map[string]any
	srv := stubServiceServer(t, `{"url":"https://gitlab.com/acme/api.git","defaultBranch":"main","path":"."}`, &patch)
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	resetServiceSetRepoFlags(t)
	defer resetServiceSetRepoFlags(t)
	const secret = "glpat-super-secret-do-not-print"
	mustSet(t, serviceSetCmd, "gitlab-token", secret)

	out := captureStdout(t, func() {
		if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}); err != nil {
			t.Fatalf("service set RunE: %v", err)
		}
	})
	if strings.Contains(out, secret) {
		t.Errorf("command echoed the GitLab token to stdout: %q", out)
	}
	// Sanity: the token still made it onto the wire.
	if got := repoBlock(t, patch)["token"]; got != secret {
		t.Errorf("repo.token = %v, want the supplied token", got)
	}
}

// TestServiceSet_TokenFromEnv covers the KUSO_GITLAB_TOKEN fallback when
// only --provider is passed (the token is not echoed and rides the wire).
func TestServiceSet_TokenFromEnv(t *testing.T) {
	var patch map[string]any
	srv := stubServiceServer(t, `{"url":"https://gitlab.com/acme/api.git","defaultBranch":"main","path":"."}`, &patch)
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	t.Setenv("KUSO_GITLAB_TOKEN", "glpat-from-env")

	resetServiceSetRepoFlags(t)
	defer resetServiceSetRepoFlags(t)
	mustSet(t, serviceSetCmd, "provider", "gitlab")

	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}); err != nil {
		t.Fatalf("service set RunE: %v", err)
	}
	if got := repoBlock(t, patch)["token"]; got != "glpat-from-env" {
		t.Errorf("repo.token = %v, want token from KUSO_GITLAB_TOKEN", got)
	}
}

// TestServiceSet_NoTokenLeavesItUnset verifies that a path/branch-only edit
// (no token supplied, no env) sends NO token field, so the server leaves any
// stored token untouched.
func TestServiceSet_NoTokenLeavesItUnset(t *testing.T) {
	var patch map[string]any
	srv := stubServiceServer(t, `{"url":"https://gitlab.com/acme/api.git","defaultBranch":"main","path":"."}`, &patch)
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()

	os.Unsetenv("KUSO_GITLAB_TOKEN")
	resetServiceSetRepoFlags(t)
	defer resetServiceSetRepoFlags(t)
	mustSet(t, serviceSetCmd, "branch", "develop")

	if err := serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}); err != nil {
		t.Fatalf("service set RunE: %v", err)
	}
	repo := repoBlock(t, patch)
	if _, ok := repo["token"]; ok {
		t.Errorf("token field present on a token-less edit; want it omitted: %+v", repo)
	}
	// URL must round-trip so the repo block isn't cleared.
	if got := repo["url"]; got != "https://gitlab.com/acme/api.git" {
		t.Errorf("repo.url not round-tripped: got %v", got)
	}
	if got := repo["branch"]; got != "develop" {
		t.Errorf("repo.branch = %v, want develop", got)
	}
}

// mustSet sets a flag (which also marks it Changed), failing the test on error.
func mustSet(t *testing.T, cmd *cobra.Command, name, val string) {
	t.Helper()
	if err := cmd.Flags().Set(name, val); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
}
