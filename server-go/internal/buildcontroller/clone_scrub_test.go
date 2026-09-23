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

	"kuso/server/internal/kube"
)

// gitHTTPServer serves a bare repo (one commit on main) over HTTPS via
// git http-backend, and returns the base URL and the commit SHA.
func gitHTTPServer(t *testing.T) (string, string) {
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
	if err := os.WriteFile(filepath.Join(work, "index.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(work, "add", ".")
	run(work, "commit", "-q", "-m", "init")
	sha := run(work, "rev-parse", "HEAD")
	run(root, "clone", "-q", "--bare", work, filepath.Join(root, "repo.git"))

	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	return srv.URL, sha
}

// runCloneScript executes the rendered clone script with /workspace
// relocated to a temp dir and returns the cloned repo's .git/config.
func runCloneScript(t *testing.T, b *kube.KusoBuild, token string) string {
	t.Helper()
	c := renderCloneContainer("b1", b)
	ws := filepath.Join(t.TempDir(), "workspace")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	script := strings.ReplaceAll(c.Args[0], "/workspace", ws)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(), "KUSO_GIT_TOKEN="+token, "GIT_SSL_NO_VERIFY=1", "HOME="+ws, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone script failed: %v\n%s", err, out)
	}
	cfg, err := os.ReadFile(filepath.Join(ws, "src", ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	return string(cfg)
}

func TestCloneScript_TokenNotLeftInGitConfig(t *testing.T) {
	base, sha := gitHTTPServer(t)
	b := baseBuild()
	b.Spec.GithubInstallationID = 1
	b.Spec.Ref = sha
	b.Spec.Repo = &kube.KusoRepoRef{URL: base + "/repo.git"}

	cfg := runCloneScript(t, b, "SECRETTOKEN123")
	if strings.Contains(cfg, "SECRETTOKEN123") {
		t.Fatalf("clone token persisted in .git/config:\n%s", cfg)
	}
	if !strings.Contains(cfg, "url = "+base+"/repo.git") {
		t.Errorf("origin should be the tokenless repo URL:\n%s", cfg)
	}
}

func TestCloneScript_URLCredentialsNotLeftInGitConfig(t *testing.T) {
	base, sha := gitHTTPServer(t)
	b := baseBuild()
	b.Spec.GithubInstallationID = 0
	b.Spec.Ref = sha
	b.Spec.Repo = &kube.KusoRepoRef{URL: strings.Replace(base, "https://", "https://deploy:URLSECRET456@", 1) + "/repo.git"}

	cfg := runCloneScript(t, b, "")
	if strings.Contains(cfg, "URLSECRET456") {
		t.Fatalf("URL credentials persisted in .git/config:\n%s", cfg)
	}
	if !strings.Contains(cfg, "url = "+base+"/repo.git") {
		t.Errorf("origin should be the repo URL without userinfo:\n%s", cfg)
	}
}

// The static site image is COPY'd from the repo root by default, so the
// build context must ignore .git or it gets served on the public domain.
func TestStaticPlan_IgnoresGitDirInContext(t *testing.T) {
	b := baseBuild()
	b.Spec.Strategy = "static"
	c := renderStaticPlanContainer(b)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("node_modules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", c.Args[0])
	cmd.Dir = dir
	for _, e := range c.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	cmd.Env = append(cmd.Env, "PATH="+os.Getenv("PATH"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("static-plan script failed: %v\n%s", err, out)
	}
	ignore, err := os.ReadFile(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		t.Fatalf("read .dockerignore: %v", err)
	}
	lines := strings.Split(string(ignore), "\n")
	if !sliceContains(lines, ".git") || !sliceContains(lines, "node_modules") {
		t.Errorf(".dockerignore must keep the repo's patterns and add .git, got %q", ignore)
	}
}

func sliceContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
