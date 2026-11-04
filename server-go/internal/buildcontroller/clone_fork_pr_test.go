package buildcontroller

import (
	"context"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/builds"
	"kuso/server/internal/kube"
)

// forkPRServer serves a bare repo whose only branch is main, plus a
// commit reachable only through refs/pull/7/head: what GitHub's base repo
// looks like for a PR opened from a fork.
func forkPRServer(t *testing.T) (url, prSHA string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	root := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command(gitBin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "HOME="+root)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	run(work, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "index.html"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "commit", "-q", "-m", "base")
	bare := filepath.Join(root, "repo.git")
	run(root, "clone", "-q", "--bare", work, bare)
	run(work, "checkout", "-q", "-b", "fork-feature")
	if err := os.WriteFile(filepath.Join(work, "index.html"), []byte("fork"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "commit", "-q", "-am", "fork change")
	prSHA = run(work, "rev-parse", "HEAD")
	run(work, "push", "-q", bare, "HEAD:refs/pull/7/head")

	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL + "/repo.git", prSHA
}

// BUILD-12: a fork PR's head branch and SHA live in the fork, not the
// base repo the build clones. The clone must fall back to the base repo's
// refs/pull/<N>/head, both when the branch is absent and when the base repo
// has a same-named branch that does not contain the SHA.
func TestCloneScript_ForkPRPreviewFetchesPullRef(t *testing.T) {
	url, sha := forkPRServer(t)
	for _, branch := range []string{"fork-feature", "main"} {
		t.Run(branch, func(t *testing.T) {
			b := baseBuild()
			b.Spec.GithubInstallationID = 0
			b.Spec.Repo = &kube.KusoRepoRef{URL: url}
			b.Spec.Branch = branch
			b.Spec.Ref = sha
			b.Annotations = map[string]string{builds.AnnPreviewPR: "7"}

			c := renderCloneContainer("b1", b)
			ws := filepath.Join(t.TempDir(), "workspace")
			if err := os.MkdirAll(ws, 0o755); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(c.Args[0], "/workspace", ws)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
			cmd.WaitDelay = time.Second
			cmd.Env = append(os.Environ(), "GIT_SSL_NO_VERIFY=1", "HOME="+ws, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("clone script failed: %v\n%s", err, out)
			}
			if !strings.Contains(string(out), "checked out: "+sha) {
				t.Errorf("did not check out the PR head %s:\n%s", sha, out)
			}
		})
	}
}
