package kusoCli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"

	"kuso/pkg/kusoApi"
)

// `service add` auto-triggers the first build, so a multi-repo project must
// be able to name the service's repo AT create — adding then `service set
// --repo` means that first build clones the project's default repo.
func TestServiceAdd_RepoAndBranchSentOnCreate(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "unexpected method "+r.Method, 405)
			return
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, "{}")
	}))
	defer srv.Close()

	api = &kusoApi.KusoClient{}
	api.Init(srv.URL, "test-token")
	defer func() { api = nil }()
	t.Cleanup(func() {
		serviceAddRepo, serviceAddBranch, serviceAddPath = "", "", "."
		for _, c := range []*cobra.Command{serviceAddCmd, serviceAddTopCmd} {
			for _, name := range []string{"repo", "branch", "path"} {
				c.Flags().Lookup(name).Changed = false
			}
		}
	})

	for _, cmd := range []*cobra.Command{serviceAddCmd, serviceAddTopCmd} {
		body = nil
		mustSet(t, cmd, "repo", "https://github.com/acme/web.git")
		mustSet(t, cmd, "branch", "develop")
		mustSet(t, cmd, "path", "apps/web")
		if err := cmd.RunE(cmd, []string{"acme", "web"}); err != nil {
			t.Fatalf("%s RunE: %v", cmd.CommandPath(), err)
		}
		repo, ok := body["repo"].(map[string]any)
		if !ok {
			t.Fatalf("%s: POST body missing repo block: %+v", cmd.CommandPath(), body)
		}
		if got := repo["url"]; got != "https://github.com/acme/web.git" {
			t.Errorf("%s: repo.url = %v", cmd.CommandPath(), got)
		}
		if got := repo["defaultBranch"]; got != "develop" {
			t.Errorf("%s: repo.defaultBranch = %v", cmd.CommandPath(), got)
		}
		if got := repo["path"]; got != "apps/web" {
			t.Errorf("%s: repo.path = %v", cmd.CommandPath(), got)
		}
	}
}
