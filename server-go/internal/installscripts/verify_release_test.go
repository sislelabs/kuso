package installscripts

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// installFuncs lifts prepare_manifests + kuso_manifest out of hack/install.sh.
func installFuncs(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "hack", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, name := range []string{"prepare_manifests", "kuso_manifest"} {
		m := regexp.MustCompile(`(?ms)^` + name + `\(\) \{.*?^\}`).Find(raw)
		if m == nil {
			t.Fatalf("%s not found in install.sh", name)
		}
		out = append(out, string(m))
	}
	return strings.Join(out, "\n")
}

func TestInstallerVerifiesSignedRelease(t *testing.T) {
	for _, tool := range []string{"bash", "openssl", "tar", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH", tool)
		}
	}
	fns := installFuncs(t)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	const base = "https://github.com/acme/kuso/releases/download/v9.9.9"

	type release struct{ tarball, releaseJSON, sig []byte }
	mk := func(t *testing.T, withBundle bool) release {
		src := t.TempDir()
		if err := os.MkdirAll(filepath.Join(src, "deploy"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, "deploy", "registry.yaml"), []byte("kind: Registry\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		tgz := filepath.Join(t.TempDir(), "b.tar.gz")
		if out, err := exec.Command("tar", "-czf", tgz, "-C", src, "deploy").CombinedOutput(); err != nil {
			t.Fatalf("tar: %v %s", err, out)
		}
		tb, _ := os.ReadFile(tgz)
		sum := sha256.Sum256(tb)
		rj := "{\n  \"version\": \"v9.9.9\",\n"
		if withBundle {
			rj += "  \"installManifests\": {\n    \"url\": \"" + base + "/install-manifests.tar.gz\",\n    \"sha256\": \"" + hex.EncodeToString(sum[:]) + "\"\n  },\n"
		}
		rj += "  \"breaking\": false\n}\n"
		sig := base64.StdEncoding.EncodeToString(ed25519.Sign(priv, []byte(rj))) + "\n"
		return release{tb, []byte(rj), []byte(sig)}
	}

	run := func(t *testing.T, r release) (string, error) {
		srv := t.TempDir()
		for name, b := range map[string][]byte{"release.json": r.releaseJSON, "release.json.sig": r.sig, "install-manifests.tar.gz": r.tarball} {
			if err := os.WriteFile(filepath.Join(srv, name), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		// curl stub: serve the release asset named by the URL's last path segment.
		script := `set -euo pipefail
log() { echo "LOG $*"; }
warn() { echo "WARN $*"; }
die() { echo "DIE $*"; exit 1; }
curl() {
  local out="" url=""
  while [[ $# -gt 0 ]]; do
    case "$1" in -o) out="$2"; shift 2 ;; -*) shift ;; *) url="$1"; shift ;; esac
  done
  [[ "$url" == "` + base + `/"* ]] || return 22
  cp "$SRV/${url##*/}" "$out"
}
` + fns + `
prepare_manifests
kuso_manifest deploy/registry.yaml
`
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = []string{
			"PATH=" + os.Getenv("PATH"), "SRV=" + srv, "TMPDIR=" + t.TempDir(),
			"KUSO_REF=v9.9.9", "KUSO_SERVER_VERSION=v9.9.9", "KUSO_REPO=acme/kuso",
			"KUSO_RAW=https://raw.example/acme/kuso/v9.9.9", "KUSO_MANIFEST_TREE=",
			"KUSO_RELEASE_PUBKEY=" + base64.StdEncoding.EncodeToString(pub),
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}

	good := mk(t, true)
	if out, err := run(t, good); err != nil || !strings.Contains(out, "kind: Registry") {
		t.Fatalf("verified release should install from its bundle: err=%v\n%s", err, out)
	}

	tampered := good
	tampered.tarball = append(append([]byte{}, good.tarball...), 0)
	if out, err := run(t, tampered); err == nil || !strings.Contains(out, "does not match the signed release") {
		t.Fatalf("swapped bundle must be refused: err=%v\n%s", err, out)
	}

	forged := good
	forged.releaseJSON = bytes.Replace(good.releaseJSON, []byte(`"breaking": false`), []byte(`"breaking": true `), 1)
	if out, err := run(t, forged); err == nil || !strings.Contains(out, "signature verification FAILED") {
		t.Fatalf("edited release.json must fail the signature: err=%v\n%s", err, out)
	}

	if out, err := run(t, mk(t, false)); err == nil || !strings.Contains(out, "KUSO_ALLOW_UNVERIFIED_MANIFESTS") {
		t.Fatalf("a release without a signed bundle must be refused with the override named: err=%v\n%s", err, out)
	}
}
