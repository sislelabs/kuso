// Package testsh picks a shell for tests that execute the backup/restore
// scripts kuso ships into containers.
//
// The scripts run in alpine (busybox ash), which supports `set -o pipefail`.
// A bare "sh" in tests is bash on macOS but dash on Ubuntu CI runners, and
// dash rejects pipefail, so tests must not rely on whatever "sh" happens to be.
package testsh

import (
	"os/exec"
	"testing"
)

// Command returns an exec.Cmd running script under a pipefail-capable shell
// (bash, else busybox sh), with flags (e.g. "-n") before "-c". It skips the
// test when neither is installed.
func Command(t testing.TB, script string, flags ...string) *exec.Cmd {
	t.Helper()
	args := append(append([]string{}, flags...), "-c", script)
	if p, err := exec.LookPath("bash"); err == nil {
		return exec.Command(p, args...)
	}
	if p, err := exec.LookPath("busybox"); err == nil {
		return exec.Command(p, append([]string{"sh"}, args...)...)
	}
	t.Skip("no pipefail-capable shell (bash or busybox) on PATH; these scripts target alpine ash")
	return nil
}
