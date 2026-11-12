package testsh

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// BackupImageRef is the tag BackupImage builds. It is deliberately not the
// published ghcr.io/sislelabs/kuso-backup:latest: running that tag with
// --platform pulled the published image over a sandbox's local build.
const BackupImageRef = "kuso-backup:localtest"

var (
	backupOnce sync.Once
	backupErr  error
	backupOut  []byte
)

// BackupImage builds build/backup from this tree for the host platform,
// once per test binary, so tests exercise the image a release would ship.
func BackupImage(t testing.TB, docker string) string {
	t.Helper()
	backupOnce.Do(func() {
		_, file, _, _ := runtime.Caller(0)
		dir := filepath.Join(filepath.Dir(file), "..", "..", "..", "build", "backup")
		backupOut, backupErr = exec.Command(docker, "build", "-q", "-t", BackupImageRef, dir).CombinedOutput()
	})
	if backupErr != nil {
		t.Fatalf("build backup image: %v\n%s", backupErr, backupOut)
	}
	return BackupImageRef
}
