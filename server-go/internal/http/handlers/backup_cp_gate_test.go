package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// DB-3 / OPS-8: the control-plane dump's opt-in gate. No Secret at all is
// a no-op; a Secret missing one key, or a missing encryption key, must fail
// the Job before pg_dump runs instead of "succeeding" with nothing (or a
// plaintext dump) uploaded.
func TestControlPlaneBackupGate(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not on PATH")
	}
	full := []string{
		"AWS_ACCESS_KEY_ID=a", "AWS_SECRET_ACCESS_KEY=s",
		"KUSO_BACKUP_BUCKET=b", "KUSO_BACKUP_ENDPOINT=https://s3.example",
	}
	cases := []struct {
		name       string
		env        []string
		wantExit   int
		want       string
		wantDumped bool
	}{
		{"no secret", nil, 0, "backup not configured", false},
		{"secret without endpoint", full[:3], 1, "missing: KUSO_BACKUP_ENDPOINT", false},
		{"secret without encryption key", full, 1, "refusing to upload an unencrypted", false},
		// The pg_dump stub fails, so the configured run exits non-zero too;
		// what matters is that it got past the gate.
		{"configured", append(append([]string{}, full...), "BACKUP_ENCRYPTION_KEY=k"), -1, "dumping kuso DB", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stubs := t.TempDir()
			marker := filepath.Join(t.TempDir(), "ran")
			if err := os.WriteFile(filepath.Join(stubs, "pg_dump"), []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			script := strings.ReplaceAll(cronJobDumpScript(t), "/backup/", t.TempDir()+"/")
			cmd := exec.Command(bash, "-c", script)
			cmd.Env = append([]string{"PATH=" + stubs + ":/usr/bin:/bin", "KUSO_DB_DSN=postgres://x@db:5432/x"}, tc.env...)
			out, err := cmd.CombinedOutput()
			code := 0
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.wantExit >= 0 && code != tc.wantExit {
				t.Errorf("exit = %d, want %d\n%s", code, tc.wantExit, out)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("output missing %q:\n%s", tc.want, out)
			}
			if _, err := os.Stat(marker); (err == nil) != tc.wantDumped {
				t.Errorf("pg_dump ran = %v, want %v\n%s", err == nil, tc.wantDumped, out)
			}
		})
	}
}
