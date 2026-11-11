package buildcontroller

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

// The push leg of the build script, run against stub buildctl/wget: a
// failure after the manifest push is a success only when the registry
// serves the digest buildkit logged as pushed.
func TestBuildScript_CacheExportFailureAfterPush(t *testing.T) {
	script := renderBuildkitContainer(baseBuild(), "dockerfile", corev1.ResourceRequirements{}).Args[0]
	i := strings.Index(script, "{ buildctl \\")
	if i < 0 {
		t.Fatal("push leg not found in build script")
	}
	tail := script[i:]
	const image = "kuso-registry.kuso.svc.cluster.local:5000/p/s:abc123"
	const pushed = "sha256:" + "81fd25f75e856ab20c73f133da39a3068ddd8db43de4d2244b969717c6996f8e"
	const other = "sha256:" + "1111111111111111111111111111111111111111111111111111111111111111"

	run := func(t *testing.T, buildctlOut string, buildctlRC int, servedDigest string) int {
		t.Helper()
		dir := t.TempDir()
		stub := func(name, body string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		stub("buildctl", "cat <<'LOG'\n"+buildctlOut+"\nLOG\nexit "+strconv.Itoa(buildctlRC)+"\n")
		stub("wget", "echo '  HTTP/1.1 200 OK' >&2\necho '  Docker-Content-Digest: "+servedDigest+"' >&2\n")
		// buildctl's rc and log go to fixed /tmp paths; parallel runs would race on them.
		cmd := exec.Command("bash", "-c", strings.ReplaceAll(tail, "/tmp/", dir+"/"))
		cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "IMAGE="+image, "INSECURE=true", "TMPDIR="+dir)
		cmd.Dir = dir
		err := cmd.Run()
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return 0
	}
	pushedLog := "#33 pushing manifest for " + image + "@" + pushed + " 0.0s done\n#33 DONE 28.1s\n#34 exporting cache to registry\nerror: failed to receive status: rpc error: code = Unavailable desc = connection reset by peer"

	if rc := run(t, pushedLog, 1, pushed); rc != 0 {
		t.Errorf("pushed image + cache export failure: rc=%d, want 0", rc)
	}
	if rc := run(t, pushedLog, 1, other); rc == 0 {
		t.Error("registry serves a different digest: must stay failed")
	}
	if rc := run(t, "#15 ERROR: process did not complete successfully", 1, pushed); rc == 0 {
		t.Error("failure before any manifest push: must stay failed")
	}
	if rc := run(t, "#33 DONE", 0, ""); rc != 0 {
		t.Errorf("clean build: rc=%d, want 0", rc)
	}
}

