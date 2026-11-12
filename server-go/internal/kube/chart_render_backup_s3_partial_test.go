package kube

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func s3BackupScript(t *testing.T) string {
	t.Helper()
	out := helmTemplateAddon(t, "s3", "backup.schedule=0 3 * * *")
	for _, doc := range strings.Split(out, "\n---\n") {
		if !strings.Contains(doc, "kind: CronJob") {
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
			t.Fatalf("decode s3 backup CronJob: %v", err)
		}
		cs := cj.Spec.JobTemplate.Spec.Template.Spec.Containers
		if len(cs) > 0 && len(cs[0].Args) > 0 && strings.Contains(cs[0].Args[0], "SRC_BUCKET") {
			return cs[0].Args[0]
		}
	}
	t.Fatalf("no s3 backup CronJob rendered:\n%s", out)
	return ""
}

// runS3Backup runs the s3 snapshot script with an rclone stub whose
// `copy` of failSrc fails; it returns the rclone calls made.
func runS3Backup(t *testing.T, script, failSrc string) ([]string, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	calls := filepath.Join(dir, "calls")
	stubs := map[string]string{
		"rclone": `echo "$*" >> ` + calls + `
case "$1" in
  copy) [ "$2" = "` + failSrc + `" ] && exit 1; exit 0 ;;
  lsf) [ "` + failSrc + `" = "lsf" ] && exit 1; echo "extra/" ;;
  rcat) cat >/dev/null ;;
esac
exit 0`,
		"aws":  "exit 0",
		"date": `echo 20261110T030000Z`,
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
		"BUCKET=b", "S3_ENDPOINT=e", "PROJECT=alpha", "ADDON=alpha-s3", "SRC_BUCKET=main", "SRC_ENDPOINT=s", "SRC_ACCESS_KEY=a", "SRC_SECRET_KEY=k",
		"AWS_ACCESS_KEY_ID=a", "AWS_SECRET_ACCESS_KEY=k",
	}
	out, _ := cmd.CombinedOutput()
	raw, _ := os.ReadFile(calls)
	return strings.Split(strings.TrimSpace(string(raw)), "\n"), string(out)
}

// BLOB-8: a copy that fails partway must not leave a manifest-less
// snapshot behind; a finished snapshot is never removed.
func TestS3Backup_FailedCopyRemovesIncompleteSnapshot(t *testing.T) {
	t.Parallel()
	script := s3BackupScript(t)
	purges := func(calls []string) []string {
		var p []string
		for _, c := range calls {
			if strings.HasPrefix(c, "purge ") {
				p = append(p, c)
			}
		}
		return p
	}

	calls, out := runS3Backup(t, script, "src:main")
	if got := purges(calls); len(got) != 1 || !strings.HasPrefix(got[0], "purge dst:b/alpha/alpha-s3/20261110T030000Z/") {
		t.Errorf("main copy failed: purges = %v, want the TS snapshot only\n%s", got, out)
	}

	calls, out = runS3Backup(t, script, "src:extra")
	if got := purges(calls); len(got) != 1 || !strings.HasPrefix(got[0], "purge dst:b/alpha/alpha-s3/20261110T030000Z.bucket-extra/") {
		t.Errorf("extra-bucket copy failed: purges = %v, want only the sibling prefix\n%s", got, out)
	}

	calls, out = runS3Backup(t, script, "none")
	if got := purges(calls); len(got) != 0 {
		t.Errorf("successful run purged %v\n%s", got, out)
	}

	// The EXIT trap also fires on success: a finished main snapshot must
	// survive a run whose extra-bucket listing fails.
	calls, out = runS3Backup(t, script, "lsf")
	if got := purges(calls); len(got) != 0 {
		t.Errorf("listing failure purged %v\n%s", got, out)
	}
}
