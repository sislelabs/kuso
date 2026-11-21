package releaserun

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/testsh"
)

// ncStub answers `nc -z -w N host port` from files in $STUB_DIR:
// <host>.fails holds how many calls fail before one succeeds,
// <host>.delay a per-call sleep, <host>.down makes every call fail.
// <host>.barrier holds N: the call blocks until N hosts have reached it, and
// logs to barrier-timeout if they never do (probes ran one after another).
const ncStub = `#!/bin/bash
echo "$*" >> "$STUB_DIR/calls"
host=${@: -2:1}
if [ -f "$STUB_DIR/$host.barrier" ]; then
  touch "$STUB_DIR/started.$host"
  want=$(cat "$STUB_DIR/$host.barrier")
  for _ in $(seq 1 30); do
    [ "$(ls "$STUB_DIR" | grep -c '^started\.')" -ge "$want" ] && exit 0
    /bin/sleep 0.1
  done
  echo "$host" >> "$STUB_DIR/barrier-timeout"
  exit 0
fi
[ -f "$STUB_DIR/$host.delay" ] && sleep "$(cat "$STUB_DIR/$host.delay")"
[ -f "$STUB_DIR/$host.down" ] && exit 1
if [ -f "$STUB_DIR/$host.fails" ]; then
  n=$(cat "$STUB_DIR/$host.fails")
  if [ "$n" -gt 0 ]; then echo $((n-1)) > "$STUB_DIR/$host.fails"; exit 1; fi
fi
exit 0
`

// sleepStub records the script's retry sleeps, then really sleeps.
const sleepStub = `#!/bin/bash
echo "$*" >> "$STUB_DIR/sleeps"
exec /bin/sleep "$@"
`

type waitRun struct {
	code    int
	stdout  string
	stderr  string
	elapsed time.Duration
	calls   string
	sleeps  string
	barrier string
}

// runWaitScript runs the wait-for-addons script under bash (busybox ash would
// run its built-in nc ahead of the PATH stub) with files seeding the stub.
func runWaitScript(t *testing.T, env []string, files map[string]string) waitRun {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "nc"), []byte(ncStub), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sleep"), []byte(sleepStub), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := testsh.Command(t, waitForAddonsScript)
	cmd.Env = append([]string{"PATH=" + dir + ":" + os.Getenv("PATH"), "STUB_DIR=" + dir}, env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	r := waitRun{elapsed: time.Since(start), stdout: stdout.String(), stderr: stderr.String()}
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		r.code = exitErr.ExitCode()
	case err != nil:
		t.Fatalf("run script: %v", err)
	}
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	r.calls = string(calls)
	sleeps, _ := os.ReadFile(filepath.Join(dir, "sleeps"))
	r.sleeps = string(sleeps)
	barrier, _ := os.ReadFile(filepath.Join(dir, "barrier-timeout"))
	r.barrier = string(barrier)
	return r
}

// A fresh pod's first connects to its DB stall until the pod's network
// policy is programmed. The old 2s sleep between attempts made every wake
// pay a whole slot for that; the retry must be sub-second.
func TestWaitForAddonsScript_RetriesSubSecond(t *testing.T) {
	t.Parallel()
	r := runWaitScript(t, []string{"DATABASE_URL=postgres://u:p@pg:5432/app"}, map[string]string{"pg.fails": "3"})
	if r.code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:%s\nstderr:%s", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stdout, "postgres reachable at pg:5432") {
		t.Errorf("missing reachable line:\n%s", r.stdout)
	}
	if got := strings.Fields(r.sleeps); len(got) != 3 {
		t.Errorf("want 3 retry sleeps after 3 failed probes, got %q", r.sleeps)
	} else {
		for _, arg := range got {
			if d, err := time.ParseDuration(arg + "s"); err != nil || d >= time.Second {
				t.Errorf("retry slept %q; the retry interval should be sub-second", arg)
			}
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(r.calls), "\n") {
		if line != "-z -w 1 pg 5432" {
			t.Errorf("nc called as %q, want a 1s connect timeout", line)
		}
	}
}

func TestWaitForAddonsScript_ProbesAddonsInParallel(t *testing.T) {
	t.Parallel()
	r := runWaitScript(t, []string{
		"DATABASE_URL=postgres://pg:5432/app",
		"REDIS_URL=redis://cache:6379",
		"NATS_URL=nats://bus:4222",
	}, map[string]string{"pg.barrier": "3", "cache.barrier": "3", "bus.barrier": "3"})
	if r.code != 0 {
		t.Fatalf("exit %d, want 0\nstdout:%s\nstderr:%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{"postgres reachable", "redis reachable", "nats reachable"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("missing %q:\n%s", want, r.stdout)
		}
	}
	if r.barrier != "" {
		t.Errorf("probes for %q waited alone; they should run concurrently", strings.Fields(r.barrier))
	}
}

// WAIT_FOR_ADDONS_ATTEMPTS keeps meaning attempts x 2s, so the chart's 15
// (30s) and the release Job's default 60 (120s) budgets are unchanged; a
// down addon fails strict mode and is waved through in soft mode.
func TestWaitForAddonsScript_BudgetAndModes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		soft     bool
		wantCode int
	}{{"strict", false, 1}, {"soft", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := []string{"DATABASE_URL=postgres://pg:5432/app", "REDIS_URL=redis://cache:6379", "WAIT_FOR_ADDONS_ATTEMPTS=1"}
			if tc.soft {
				env = append(env, "WAIT_FOR_ADDONS_SOFT=1")
			}
			r := runWaitScript(t, env, map[string]string{"pg.down": ""})
			if r.code != tc.wantCode {
				t.Fatalf("exit %d, want %d\nstdout:%s\nstderr:%s", r.code, tc.wantCode, r.stdout, r.stderr)
			}
			if !strings.Contains(r.stderr, "postgres at pg:5432 not reachable after 2s") {
				t.Errorf("want a 2s budget for 1 attempt:\n%s", r.stderr)
			}
			if !strings.Contains(r.stdout, "redis reachable") {
				t.Errorf("a reachable addon should still report reachable:\n%s", r.stdout)
			}
			// date +%s has 1s resolution, so a 2s budget ends 1-2s in.
			if r.elapsed < time.Second || r.elapsed > 6*time.Second { // x4 budget would be 8s
				t.Errorf("gave up after %s, want the 2s budget", r.elapsed)
			}
		})
	}
}

func TestWaitForAddonsScript_NothingToWaitFor(t *testing.T) {
	t.Parallel()
	r := runWaitScript(t, nil, nil)
	if r.code != 0 || r.calls != "" {
		t.Fatalf("exit %d calls %q, want 0 and no probes", r.code, r.calls)
	}
}
