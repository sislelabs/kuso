package kube

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// inClusterBackupScript renders the kusoaddon chart for a plain in-cluster
// postgres addon with a backup schedule and returns the dump script.
func inClusterBackupScript(t *testing.T) string {
	t.Helper()
	out := helmTemplateAddon(t, "postgres", "backup.schedule=0 3 * * *")
	for _, doc := range strings.Split(out, "\n---\n") {
		if !strings.Contains(doc, "kind: CronJob") || strings.Contains(doc, `kuso.sislelabs.com/external: "true"`) {
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
			t.Fatalf("decode in-cluster backup CronJob: %v", err)
		}
		cs := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
		if len(cs) == 0 || len(cs[0].Args) == 0 {
			t.Fatalf("in-cluster backup CronJob has no container args:\n%s", doc)
		}
		if !strings.Contains(cs[0].Args[0], "POSTGRES_HOST") {
			continue
		}
		return cs[0].Args[0]
	}
	t.Fatalf("no in-cluster postgres backup CronJob rendered:\n%s", out)
	return ""
}

// The in-cluster branch swallowed a failed version probe (`2>/dev/null ...
// || true`) and fell back to the newest pg_dump, whose SET
// transaction_timeout makes the dump unrestorable into PG16. Same contract
// as the external branch: fail loudly, never dump blind.
func TestInClusterPostgresBackup_VersionProbeGatesDump(t *testing.T) {
	t.Parallel()
	s := inClusterBackupScript(t)
	if wait, probe := strings.Index(s, `nc -z -w2 "${POSTGRES_HOST}" 5432`), strings.Index(s, "SHOW server_version_num"); wait < 0 || probe < 0 || wait > probe {
		t.Fatalf("reachability wait (at %d) must precede the version probe (at %d)", wait, probe)
	}
	cases := []struct {
		name       string
		nc         string
		psql       string
		wantDump   bool
		wantOutput string
	}{
		{"probe ok", "exit 0", `case "$*" in *server_version_num*) echo 160004;; esac`, true, "server major=16"},
		{"probe fails", "exit 0", `echo "psql: connection refused" >&2; exit 2`, false, "refusing to pick a pg_dump client"},
		{"probe garbage", "exit 0", `case "$*" in *server_version_num*) echo nope;; esac`, false, "unparseable server_version_num"},
		{"never reachable", "exit 1", "echo 160004", false, "unreachable after 60s"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, dumped, err := runInClusterBackup(t, s, tc.nc, tc.psql)
			if dumped != tc.wantDump {
				t.Errorf("pg_dump ran = %v, want %v:\n%s", dumped, tc.wantDump, out)
			}
			if tc.wantDump && err != nil {
				t.Errorf("run failed: %v\n%s", err, out)
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

func runInClusterBackup(t *testing.T, script, nc, psql string) (string, bool, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "dumped")
	stubs := map[string]string{
		"nc":      nc,
		"psql":    psql,
		"pg_dump": "touch " + marker + "; echo 'select 1;'",
		"aws":     "exit 0",
		"sleep":   "exit 0",
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"BUCKET=b", "S3_ENDPOINT=e", "PROJECT=alpha", "ADDON=alpha-postgres",
		"POSTGRES_HOST=alpha-postgres", "POSTGRES_USER=kuso", "POSTGRES_DB=alpha",
		"POSTGRES_PASSWORD=pw", "RETENTION_DAYS=14",
	}
	out, err := cmd.CombinedOutput()
	_, statErr := os.Stat(marker)
	return string(out), statErr == nil, err
}
