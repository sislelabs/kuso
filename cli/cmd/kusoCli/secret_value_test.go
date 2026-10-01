package kusoCli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSecretValue(t *testing.T) {
	orig, origTTY := secretStdin, stdinIsTTYFn
	t.Cleanup(func() { secretStdin, stdinIsTTYFn = orig, origTTY })
	stdinIsTTYFn = func() bool { return false }

	secretStdin = strings.NewReader("s3cr3t\n")
	if v, err := resolveSecretValue("-", true, ""); err != nil || v != "s3cr3t" {
		t.Errorf("stdin: %q %v", v, err)
	}
	f := filepath.Join(t.TempDir(), "key.pem")
	_ = os.WriteFile(f, []byte("-----BEGIN-----\nx\n"), 0o600)
	if v, err := resolveSecretValue("", false, f); err != nil || v != "-----BEGIN-----\nx\n" {
		t.Errorf("file: %q %v", v, err)
	}
	if _, err := resolveSecretValue("lit", true, f); err == nil {
		t.Error("inline value plus --from-file must be refused")
	}
	if _, err := resolveSecretValue("", false, ""); err == nil {
		t.Error("no value at all must be refused")
	}
	k, v, err := splitSecretKV("TOKEN=a=b==", "")
	if err != nil || k != "TOKEN" || v != "a=b==" {
		t.Errorf("KEY=VALUE split on first '=': %q %q %v", k, v, err)
	}
	k, v, err = splitSecretKV("SA_JSON", f)
	if err != nil || k != "SA_JSON" || !strings.HasPrefix(v, "-----BEGIN") {
		t.Errorf("bare KEY with --from-file: %q %q %v", k, v, err)
	}
}
