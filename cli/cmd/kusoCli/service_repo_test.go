package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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

// TestServiceSetRepo drives `service set`'s repo path against a stub that
// serves the current repo and records the PATCH body.
func TestServiceSetRepo(t *testing.T) {
	const secret = "glpat-super-secret-do-not-print"
	for _, tc := range []struct {
		name    string
		current string
		flags   [][2]string
		envTok  string // KUSO_GITLAB_TOKEN
		wantErr string
		// want: repo-block fields that must be present with these values.
		want map[string]any
		// absent: repo-block fields that must NOT be sent.
		absent []string
		// noEcho: a string that must never reach stdout.
		noEcho string
	}{
		// --repo --provider --gitlab-token: the PATCH carries url + provider + token.
		{
			name:    "GitlabRepoWithToken",
			current: `{"url":"https://github.com/old/repo.git","defaultBranch":"main","path":"."}`,
			flags:   [][2]string{{"repo", "https://gitlab.com/acme/api.git"}, {"provider", "gitlab"}, {"gitlab-token", "glpat-secret-value"}},
			want:    map[string]any{"url": "https://gitlab.com/acme/api.git", "provider": "gitlab", "token": "glpat-secret-value"},
		},
		// Provider is lower-cased ...
		{
			name:  "ProviderNormalised",
			flags: [][2]string{{"provider", "GitLab"}},
			want:  map[string]any{"provider": "gitlab"},
		},
		// ... and rejected when not github|gitlab, before any PATCH is sent.
		{
			name:    "ProviderValidated",
			flags:   [][2]string{{"provider", "bitbucket"}},
			wantErr: "must be github|gitlab",
		},
		// The supplied token must not appear in anything written to stdout,
		// but still rides the wire.
		{
			name:   "TokenNeverEchoed",
			flags:  [][2]string{{"gitlab-token", secret}},
			want:   map[string]any{"token": secret},
			noEcho: secret,
		},
		// KUSO_GITLAB_TOKEN fallback when only --provider is passed.
		{
			name:   "TokenFromEnv",
			flags:  [][2]string{{"provider", "gitlab"}},
			envTok: "glpat-from-env",
			want:   map[string]any{"token": "glpat-from-env"},
		},
		// A path/branch-only edit (no token supplied, no env) sends NO token
		// field, so the server leaves any stored token untouched. The URL must
		// round-trip so the repo block isn't cleared.
		{
			name:   "NoTokenLeavesItUnset",
			flags:  [][2]string{{"branch", "develop"}},
			want:   map[string]any{"url": "https://gitlab.com/acme/api.git", "branch": "develop"},
			absent: []string{"token"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := tc.current
			if current == "" {
				current = `{"url":"https://gitlab.com/acme/api.git","defaultBranch":"main","path":"."}`
			}
			var patch map[string]any
			srv := stubServiceServer(t, current, &patch)
			defer srv.Close()
			api = &kusoApi.KusoClient{}
			api.Init(srv.URL, "test-token")
			defer func() { api = nil }()
			t.Setenv("KUSO_GITLAB_TOKEN", tc.envTok)
			t.Setenv("KUSO_REPO_TOKEN", "")

			resetServiceSetRepoFlags(t)
			defer resetServiceSetRepoFlags(t)
			for _, f := range tc.flags {
				mustSet(t, serviceSetCmd, f[0], f[1])
			}

			var err error
			out := captureStdout(t, func() { err = serviceSetCmd.RunE(serviceSetCmd, []string{"acme", "api"}) })
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected %q error, got %v", tc.wantErr, err)
				}
				if patch != nil {
					t.Fatalf("PATCH sent despite validation error: %+v", patch)
				}
				return
			}
			if err != nil {
				t.Fatalf("service set RunE: %v", err)
			}
			if tc.noEcho != "" && strings.Contains(out, tc.noEcho) {
				t.Errorf("command echoed the GitLab token to stdout: %q", out)
			}
			repo := repoBlock(t, patch)
			for k, v := range tc.want {
				if repo[k] != v {
					t.Errorf("repo.%s = %v, want %v", k, repo[k], v)
				}
			}
			for _, k := range tc.absent {
				if _, ok := repo[k]; ok {
					t.Errorf("%s field present; want it omitted: %+v", k, repo)
				}
			}
		})
	}
}

// mustSet sets a flag (which also marks it Changed), failing the test on error.
func mustSet(t *testing.T, cmd *cobra.Command, name, val string) {
	t.Helper()
	if err := cmd.Flags().Set(name, val); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
}
