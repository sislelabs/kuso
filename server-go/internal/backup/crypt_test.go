package backup

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 15, 16, 17, 4096, 64*1024 - 1, 64 * 1024, 200_003} {
		plain := randBytes(t, n)
		var ct bytes.Buffer
		if err := Encrypt(&ct, bytes.NewReader(plain), "k3y"); err != nil {
			t.Fatalf("n=%d encrypt: %v", n, err)
		}
		if bytes.Contains(ct.Bytes(), plain) && n > 16 {
			t.Fatalf("n=%d ciphertext contains plaintext", n)
		}
		r, enc, err := MaybeDecrypt(bytes.NewReader(ct.Bytes()), "k3y")
		if err != nil || !enc {
			t.Fatalf("n=%d decrypt: enc=%v err=%v", n, enc, err)
		}
		got, err := io.ReadAll(iotest1(r))
		if err != nil {
			t.Fatalf("n=%d read: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("n=%d round trip mismatch (got %d bytes)", n, len(got))
		}
	}
}

// iotest1 reads one byte at a time to exercise the buffering edges.
func iotest1(r io.Reader) io.Reader { return oneByte{r} }

type oneByte struct{ r io.Reader }

func (o oneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}

// Every dump written before encryption shipped is a bare gzip stream and
// must still come back unchanged, with or without a key configured.
func TestMaybeDecryptLegacyPlaintextPassesThrough(t *testing.T) {
	var gz bytes.Buffer
	w := gzip.NewWriter(&gz)
	_, _ = w.Write([]byte("CREATE TABLE t (id int);\n"))
	_ = w.Close()
	for _, key := range []string{"", "some-key"} {
		r, enc, err := MaybeDecrypt(bytes.NewReader(gz.Bytes()), key)
		if err != nil || enc {
			t.Fatalf("key=%q: enc=%v err=%v", key, enc, err)
		}
		got, _ := io.ReadAll(r)
		if !bytes.Equal(got, gz.Bytes()) {
			t.Fatalf("key=%q: legacy dump altered", key)
		}
	}
}

func TestMaybeDecryptRefusesWithoutOrWithWrongKey(t *testing.T) {
	var ct bytes.Buffer
	if err := Encrypt(&ct, strings.NewReader(strings.Repeat("row\n", 1000)), "right"); err != nil {
		t.Fatal(err)
	}
	if _, enc, err := MaybeDecrypt(bytes.NewReader(ct.Bytes()), ""); !enc || !errors.Is(err, ErrEncryptedNoKey) {
		t.Fatalf("no key: enc=%v err=%v", enc, err)
	}
	r, _, err := MaybeDecrypt(bytes.NewReader(ct.Bytes()), "wrong")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err == nil && bytes.Equal(got, []byte(strings.Repeat("row\n", 1000))) {
		t.Fatal("wrong key decrypted to the plaintext")
	}
}

// The CronJob encrypts with the openssl CLI; the server must read that
// and openssl must read ours. Skipped when no OpenSSL 3 binary is present.
func TestOpenSSLCompatibility(t *testing.T) {
	bin, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl not installed")
	}
	if out, _ := exec.Command(bin, "version").Output(); !strings.HasPrefix(string(out), "OpenSSL 3") {
		t.Skipf("need OpenSSL 3 for -pbkdf2, have %q", out)
	}
	plain := randBytes(t, 100_001)
	args := []string{"enc", "-aes-256-cbc", "-pbkdf2", "-iter", strconv.Itoa(PBKDF2Iter), "-md", "sha256", "-pass", "env:BACKUP_ENCRYPTION_KEY"}
	env := append(os.Environ(), "BACKUP_ENCRYPTION_KEY=s3cret")

	enc := exec.Command(bin, append(args, "-salt")...)
	enc.Env, enc.Stdin = env, bytes.NewReader(plain)
	ct, err := enc.Output()
	if err != nil {
		t.Fatalf("openssl encrypt: %v", err)
	}
	r, isEnc, err := MaybeDecrypt(bytes.NewReader(ct), "s3cret")
	if err != nil || !isEnc {
		t.Fatalf("decrypt openssl output: enc=%v err=%v", isEnc, err)
	}
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("openssl→go mismatch: err=%v len=%d", err, len(got))
	}

	var ours bytes.Buffer
	if err := Encrypt(&ours, bytes.NewReader(plain), "s3cret"); err != nil {
		t.Fatal(err)
	}
	dec := exec.Command(bin, append(args, "-d")...)
	dec.Env, dec.Stdin = env, bytes.NewReader(ours.Bytes())
	back, err := dec.Output()
	if err != nil || !bytes.Equal(back, plain) {
		t.Fatalf("go→openssl mismatch: err=%v len=%d", err, len(back))
	}
}
