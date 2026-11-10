package backup

import (
	"bytes"
	"compress/gzip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The restore Job's s3_fetch must hand the restore the plaintext gzip for
// both an encrypted dump and a legacy plaintext one. Runs the real shell
// with `aws` stubbed to copy a local file.
func TestS3FetchShellDecryptsAndPassesLegacy(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not installed")
	}
	if out, err := exec.Command("openssl", "version").Output(); err != nil || !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skip("need OpenSSL 3")
	}
	dir := t.TempDir()
	stub := filepath.Join(dir, "bin")
	if err := os.Mkdir(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	// aws s3 cp --endpoint-url X <src> <dst>: src is s3://bucket/<file in dir>
	awsStub := "#!/usr/bin/env bash\nsrc=\"$5\"; cp \"" + dir + "/${src#s3://bucket/}\" \"$6\"\n"
	if err := os.WriteFile(filepath.Join(stub, "aws"), []byte(awsStub), 0o755); err != nil {
		t.Fatal(err)
	}

	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte("CREATE TABLE t (id int);\n"))
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(dir, "legacy.sql.gz"), gz.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	var ct bytes.Buffer
	if err := Encrypt(&ct, bytes.NewReader(gz.Bytes()), "the-key"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "enc.sql.gz"), ct.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(src, key string) ([]byte, error) {
		out := filepath.Join(dir, "out-"+src)
		cmd := exec.Command(bash, "-c", "set -eo pipefail\n"+s3FetchShell+"\ns3_fetch s3://bucket/"+src+" "+out)
		cmd.Env = append(os.Environ(), "PATH="+stub+":"+os.Getenv("PATH"), "S3_ENDPOINT=x", "BACKUP_ENCRYPTION_KEY="+key)
		if b, err := cmd.CombinedOutput(); err != nil {
			return b, err
		}
		return os.ReadFile(out)
	}
	for _, src := range []string{"legacy.sql.gz", "enc.sql.gz"} {
		got, err := run(src, "the-key")
		if err != nil {
			t.Fatalf("%s: %v: %s", src, err, got)
		}
		if !bytes.Equal(got, gz.Bytes()) {
			t.Errorf("%s: restore input is not the plaintext gzip", src)
		}
	}
	if out, err := run("enc.sql.gz", ""); err == nil || !strings.Contains(string(out), "no backup encryption key") {
		t.Errorf("encrypted dump without a key should fail loudly, got err=%v out=%s", err, out)
	}
	// openssl rejects a wrong key on padding ~255/256 of the time; either
	// way it must never yield the plaintext.
	if got, err := run("enc.sql.gz", "wrong-key"); err == nil && bytes.Equal(got, gz.Bytes()) {
		t.Error("wrong key produced the plaintext")
	}
}
