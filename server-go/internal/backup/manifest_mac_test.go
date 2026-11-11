package backup

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// chartSealFn extracts the seal() shell function the backup CronJobs run
// from the kusoaddon chart, so the test signs exactly what production signs.
func chartSealFn(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	b, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "..", "operator", "helm-charts", "kusoaddon", "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	start := strings.Index(s, "\nseal() {")
	end := strings.Index(s[start:], "\n}\n")
	if start < 0 || end < 0 {
		t.Fatal("seal() not found in _helpers.tpl")
	}
	return s[start : start+end+3]
}

// BLOB-5: the manifest's bare sha256 sits beside the artifact, so a bucket
// writer can replace both. The chart now signs it under the addon's backup
// key; the restore must accept that signature and refuse a forged one.
func TestManifestMac_ChartSignsRestoreVerifies(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	if out, err := exec.Command("openssl", "version").Output(); err != nil || !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skip("need OpenSSL 3")
	}
	dir := t.TempDir()
	art := filepath.Join(dir, "dump.gz")
	if err := os.WriteFile(art, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	seal := exec.Command(bash, "-c", chartSealFn(t)+`
seal "$1"
printf '{"artifacts":[{"key":"k","sha256":"%s"%s}]}' "$SHA" "$ENC_JSON"`, "bash", art)
	seal.Env = append(os.Environ(), "BACKUP_ENCRYPTION_KEY=the-key")
	manifest, err := seal.Output()
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.Contains(string(manifest), `"mac":"`) {
		t.Fatalf("sealed manifest has no mac: %s", manifest)
	}
	// seal encrypted the artifact in place; recover the plaintext digest
	// the manifest recorded.
	sha := strings.Split(strings.Split(string(manifest), `"sha256":"`)[1], `"`)[0]

	plain := filepath.Join(dir, "plain")
	if err := os.WriteFile(plain, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	verify := func(manifest, key string) (string, error) {
		src := filepath.Join(dir, "manifest.src")
		if err := os.WriteFile(src, []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		// aws s3 cp --endpoint-url E <src> <dst> "downloads" the manifest.
		script := `aws() { cp "` + src + `" "$6"; }` + "\n" +
			strings.ReplaceAll(verifyManifestShell, "/tmp/manifest.json", filepath.Join(dir, "manifest.json")) +
			"\nverify_manifest " + plain
		cmd := exec.Command(bash, "-c", "set -eo pipefail\n"+script)
		cmd.Env = append(os.Environ(), "BACKUP_ENCRYPTION_KEY="+key, "ALLOW_UNVERIFIED=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := verify(string(manifest), "the-key"); err != nil || !strings.Contains(out, "signature OK") {
		t.Fatalf("chart-signed manifest rejected: err=%v out=%s", err, out)
	}
	if out, err := verify(string(manifest), "other-key"); err == nil || !strings.Contains(out, "MISMATCH") {
		t.Fatalf("manifest signed with another key accepted: err=%v out=%s", err, out)
	}
	forged := strings.Replace(string(manifest), `"mac":"`, `"mac":"00`, 1)
	if out, err := verify(forged, "the-key"); err == nil {
		t.Fatalf("forged mac accepted: %s", out)
	}
	// A manifest from before the mac existed keeps the sha256-only check.
	if out, err := verify(`{"artifacts":[{"key":"k","sha256":"`+sha+`"}]}`, "the-key"); err != nil {
		t.Fatalf("pre-mac manifest rejected: err=%v out=%s", err, out)
	}
}
