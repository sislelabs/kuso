package incidents

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// OPS-1: the implement agent can write .git/config and hooks, and git
// runs programs those name. Each vector must fire against plain git (so
// the case is real) and stay inert behind git-guard.sh.
func TestGitGuardNeutralisesExecutors(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	guard, err := filepath.Abs("../../../build/incident-agent/git-guard.sh")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	baseEnv := append(os.Environ(), "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Env = baseEnv
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(path, body string, mode os.FileMode) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}

	type vector struct {
		name    string
		setup   func(repo, payload string)
		args    []string
		env     []string
		refused bool // the guard should refuse outright, not just neutralise
	}
	vectors := []vector{
		{name: "core.fsmonitor", args: []string{"status"}, refused: true,
			setup: func(r, p string) { git(r, "config", "core.fsmonitor", p) }},
		{name: "diff.external", args: []string{"diff"}, refused: true,
			setup: func(r, p string) { git(r, "config", "diff.external", p) }},
		{name: "textconv driver", args: []string{"diff"}, refused: true,
			setup: func(r, p string) {
				write(filepath.Join(r, ".gitattributes"), "*.txt diff=pwn\n", 0o644)
				git(r, "config", "diff.pwn.textconv", p)
			}},
		{name: "clean filter", args: []string{"add", "a.txt"}, refused: true,
			setup: func(r, p string) {
				write(filepath.Join(r, ".gitattributes"), "*.txt filter=pwn\n", 0o644)
				git(r, "config", "filter.pwn.clean", p)
			}},
		{name: "include.path", args: []string{"status"}, refused: true,
			setup: func(r, p string) {
				inc := filepath.Join(r, "inc.cfg")
				write(inc, "[core]\n\tfsmonitor = "+p+"\n", 0o644)
				git(r, "config", "include.path", inc)
			}},
		{name: "core.hooksPath", args: []string{"commit", "-qm", "x"}, refused: true,
			setup: func(r, p string) {
				write(filepath.Join(r, "hooks", "pre-commit"), "#!/bin/sh\nexec "+p+"\n", 0o755)
				git(r, "config", "core.hooksPath", filepath.Join(r, "hooks"))
				git(r, "add", "a.txt")
			}},
		{name: "default hooks dir", args: []string{"commit", "-qm", "x"},
			setup: func(r, p string) {
				write(filepath.Join(r, ".git", "hooks", "pre-commit"), "#!/bin/sh\nexec "+p+"\n", 0o755)
				git(r, "add", "a.txt")
			}},
		{name: "GIT_EXTERNAL_DIFF", args: []string{"diff"},
			setup: func(r, p string) {}, env: []string{"GIT_EXTERNAL_DIFF=PAYLOAD"}},
		{name: "-c global option", args: []string{"-c", "core.fsmonitor=PAYLOAD", "status"}, refused: true,
			setup: func(r, p string) {}},
	}

	for _, v := range vectors {
		t.Run(v.name, func(t *testing.T) {
			run := func(guarded bool) (marker string, err error) {
				dir := t.TempDir()
				repo := filepath.Join(dir, "repo")
				marker = filepath.Join(dir, "pwned")
				payload := filepath.Join(dir, "payload.sh")
				write(payload, "#!/bin/sh\ntouch "+marker+"\ncat 2>/dev/null\nexit 0\n", 0o755)
				git(dir, "init", "-q", repo)
				write(filepath.Join(repo, "a.txt"), "one\n", 0o644)
				git(repo, "add", "a.txt")
				git(repo, "commit", "-qm", "init")
				write(filepath.Join(repo, "a.txt"), "two\n", 0o644)
				v.setup(repo, payload)

				args := make([]string, len(v.args))
				for i, a := range v.args {
					args[i] = replacePayload(a, payload)
				}
				var cmd *exec.Cmd
				if guarded {
					cmd = exec.Command("bash", append([]string{guard}, args...)...)
				} else {
					cmd = exec.Command(gitBin, args...)
				}
				cmd.Dir = repo
				cmd.Env = append(append([]string{}, baseEnv...), "KUSO_GIT_REAL="+gitBin)
				if !guarded {
					cmd.Env = append(cmd.Env, "GIT_CONFIG_COUNT=2",
						"GIT_CONFIG_KEY_0=user.name", "GIT_CONFIG_VALUE_0=t",
						"GIT_CONFIG_KEY_1=user.email", "GIT_CONFIG_VALUE_1=t@t")
				}
				for _, e := range v.env {
					cmd.Env = append(cmd.Env, replacePayload(e, payload))
				}
				_, err = cmd.CombinedOutput()
				return marker, err
			}

			marker, _ := run(false)
			if _, err := os.Stat(marker); err != nil {
				t.Fatalf("vector did not fire against plain git; the case proves nothing")
			}
			marker, err := run(true)
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("payload ran through the guard")
			}
			if v.refused && err == nil {
				t.Errorf("guard ran git instead of refusing")
			}
			if !v.refused && err != nil {
				t.Errorf("guard broke a legitimate call: %v", err)
			}
		})
	}
}

// A freshly cloned checkout must keep working through the guard: the
// agent needs status, diff, add and commit.
func TestGitGuardAllowsCloneConfig(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not available")
	}
	guard, _ := filepath.Abs("../../../build/incident-agent/git-guard.sh")
	dir := t.TempDir()
	env := append(os.Environ(), "HOME="+dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "KUSO_GIT_REAL="+gitBin)
	runIn := func(wd string, bin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(bin, args...)
		cmd.Dir, cmd.Env = wd, env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", bin, args, err, out)
		}
		return string(out)
	}
	origin := filepath.Join(dir, "origin")
	runIn(dir, gitBin, "init", "-q", "-b", "main", origin)
	if err := os.WriteFile(filepath.Join(origin, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(origin, gitBin, "add", "a.txt")
	runIn(origin, gitBin, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "init")
	runIn(dir, gitBin, "clone", "-q", "--branch", "main", "file://"+origin, "repo")
	repo := filepath.Join(dir, "repo")
	runIn(repo, gitBin, "checkout", "-q", "-b", "fix")

	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runIn(repo, "bash", guard, "--no-optional-locks", "status", "--short")
	runIn(repo, "bash", guard, "diff")
	runIn(repo, "bash", guard, "add", "a.txt")
	runIn(repo, "bash", guard, "commit", "-qm", "fix")
	if got := runIn(repo, "bash", guard, "log", "-1", "--format=%an"); got != "kuso-incident-agent\n" {
		t.Errorf("commit author = %q", got)
	}
}

func replacePayload(s, payload string) string { return strings.ReplaceAll(s, "PAYLOAD", payload) }
