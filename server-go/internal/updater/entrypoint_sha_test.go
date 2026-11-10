package updater

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// verifySHAFunc lifts verify_sha256 out of the updater entrypoint so its
// policy can run without the rest of the script (kubectl, curl).
func verifySHAFunc(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "build", "updater", "entrypoint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?ms)^verify_sha256\(\) \{.*?^\}`).Find(raw)
	if m == nil {
		t.Fatal("verify_sha256 not found in entrypoint.sh")
	}
	return string(m)
}

func TestEntrypointMissingSHAIsHardFailure(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not on PATH")
	}
	fn := verifySHAFunc(t)
	asset := filepath.Join(t.TempDir(), "crds.yaml")
	if err := os.WriteFile(asset, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(allow string) (string, error) {
		script := "set -eu\nwrite_status() { echo \"STATUS $1: $2\"; }\nUNVERIFIED=\"\"\n" + fn +
			"\nverify_sha256 \"$1\" \"\" crds.yaml\necho APPLIED\n"
		cmd := exec.Command("bash", "-c", script, "bash", asset)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "KUSO_ALLOW_UNVERIFIED_ASSETS=" + allow}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := run("")
	if err == nil || strings.Contains(out, "APPLIED") || !strings.Contains(out, "STATUS failed") {
		t.Fatalf("missing sha256 must fail before applying; err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "--allow-unverified") {
		t.Errorf("failure status must name the override: %s", out)
	}
	out, err = run("1")
	if err != nil || !strings.Contains(out, "APPLIED") {
		t.Fatalf("override must let it through; err=%v out=%s", err, out)
	}
}
