package kube

import (
	"os/exec"
	"testing"
)

// pipefailShell returns argv for running a backup script that uses
// `set -o pipefail`. Production runs these under busybox ash (alpine);
// plain `sh` is dash on Ubuntu CI runners, which rejects pipefail, so
// prefer busybox, then bash, else skip.
func pipefailShell(t *testing.T, script string) *exec.Cmd {
	t.Helper()
	if bb, err := exec.LookPath("busybox"); err == nil {
		return exec.Command(bb, "sh", "-c", script)
	}
	if bash, err := exec.LookPath("bash"); err == nil {
		return exec.Command(bash, "-c", script)
	}
	t.Skip("no busybox or bash on PATH; pipefail scripts need one")
	return nil
}
