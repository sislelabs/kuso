package kube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// externalBackupScript renders the kusoaddon chart for an external postgres
// addon with a backup schedule and returns the dump container's script.
func externalBackupScript(t *testing.T) string {
	t.Helper()
	out := helmTemplateAddon(t, "postgres", "external.secretName=src", "backup.schedule=0 3 * * *")
	for _, doc := range strings.Split(out, "\n---\n") {
		if !strings.Contains(doc, "kind: CronJob") || !strings.Contains(doc, `kuso.sislelabs.com/external: "true"`) {
			continue
		}
		var cj struct {
			Spec struct {
				JobTemplate struct {
					Spec struct {
						Template struct {
							Spec struct {
								Containers []struct {
									Args []string `yaml:"args"`
								} `yaml:"containers"`
							} `yaml:"spec"`
						} `yaml:"template"`
					} `yaml:"spec"`
				} `yaml:"jobTemplate"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cj); err != nil {
			t.Fatalf("decode external backup CronJob: %v", err)
		}
		cs := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
		if len(cs) == 0 || len(cs[0].Args) == 0 {
			t.Fatalf("external backup CronJob has no container args:\n%s", doc)
		}
		return cs[0].Args[0]
	}
	t.Fatalf("no external postgres backup CronJob rendered:\n%s", out)
	return ""
}

// A fresh backup pod is network-isolated for ~5-20s until kube-router syncs
// the NetworkPolicy for its IP. The server-version probe must not be the
// first thing to touch the network.
func TestExternalPostgresBackup_WaitsBeforeVersionProbe(t *testing.T) {
	t.Parallel()
	s := externalBackupScript(t)
	wait := strings.Index(s, `pg_isready -d "${DATABASE_URL}"`)
	probe := strings.Index(s, "SHOW server_version_num")
	if wait < 0 || probe < 0 || wait > probe {
		t.Fatalf("pg_isready wait (at %d) must precede the version probe (at %d):\n%s", wait, probe, s)
	}
}

// A failed version probe used to fall back to the newest pg_dump, which
// writes SET transaction_timeout that a PG16 server rejects on restore: a
// "successful" backup that cannot be restored. The run must fail instead,
// and pg_dump must never start.
func TestExternalPostgresBackup_VersionProbeGatesDump(t *testing.T) {
	t.Parallel()
	s := externalBackupScript(t)
	cases := []struct {
		name       string
		pgIsReady  string
		psql       string
		wantDump   bool
		wantOutput string
	}{
		{"probe ok", "exit 0", "echo 160004", true, "external server major=16"},
		{"probe fails", "exit 0", `echo "psql: connection refused" >&2; exit 2`, false, "refusing to pick a pg_dump client"},
		{"probe garbage", "exit 0", "echo nope", false, "unparseable server_version_num"},
		{"never reachable", "exit 2", "echo 160004", false, "unreachable after 60s"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, dumped, err := runExternalBackup(t, s, tc.pgIsReady, tc.psql)
			if dumped != tc.wantDump {
				t.Errorf("pg_dump ran = %v, want %v:\n%s", dumped, tc.wantDump, out)
			}
			if !tc.wantDump && err == nil {
				t.Errorf("run exited 0 without dumping:\n%s", out)
			}
			if !strings.Contains(out, tc.wantOutput) {
				t.Errorf("output missing %q:\n%s", tc.wantOutput, out)
			}
		})
	}
}

// runExternalBackup executes the rendered script against stubbed client
// binaries and reports whether pg_dump was invoked.
func runExternalBackup(t *testing.T, script, pgIsReady, psql string) (string, bool, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "dumped")
	stubs := map[string]string{
		"pg_isready": pgIsReady,
		"psql":       psql,
		"pg_dump":    "touch " + marker + "; echo 'select 1;'",
		"aws":        "exit 0",
		"sleep":      "exit 0",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := pipefailShell(t, script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"BUCKET=b", "S3_ENDPOINT=e", "PROJECT=alpha", "ADDON=alpha-postgres",
		"DATABASE_URL=postgres://u:p@db.example.com:5432/app", "RETENTION_DAYS=14",
	}
	out, err := cmd.CombinedOutput()
	_, statErr := os.Stat(marker)
	return string(out), statErr == nil, err
}
