package kube

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/backup"
)

const (
	minioImage = "pgsty/minio:RELEASE.2026-08-04T00-00-00Z"
	mcImage    = "pgsty/mc:RELEASE.2026-10-26T00-00-00Z"
)

// renderedS3SnapshotScript is the s3 backup CronJob's shell as the chart
// renders it.
func renderedS3SnapshotScript(t *testing.T) string {
	t.Helper()
	out := helmTemplateAddon(t, "s3", "backup.schedule=0 3 * * *", "backup.retentionDays=0")
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
									Name string   `yaml:"name"`
									Args []string `yaml:"args"`
								} `yaml:"containers"`
							} `yaml:"spec"`
						} `yaml:"template"`
					} `yaml:"spec"`
				} `yaml:"jobTemplate"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &cj); err != nil {
			t.Fatalf("decode CronJob: %v", err)
		}
		if cs := cj.Spec.JobTemplate.Spec.Template.Spec.Containers; len(cs) > 0 && cs[0].Name == "sync" {
			return cs[0].Args[0]
		}
	}
	t.Fatal("no s3 snapshot CronJob rendered")
	return ""
}

// The s3 snapshot backup and restore, run in the real images against two
// real MinIOs (addon + backup store): an encrypted snapshot must not hold
// plaintext, must restore byte-identical with the key and refuse without
// it, and a plaintext snapshot (no key) must still restore (BLOB-6).
func TestS3Snapshot_EncryptedRoundTrip_Docker(t *testing.T) {
	docker := requireBackupImage(t)
	ensureImage(t, docker, minioImage)
	ensureImage(t, docker, mcImage)
	script := renderedS3SnapshotScript(t)
	id := fmt.Sprintf("kuso-s3rt-%d", time.Now().UnixNano())
	run := func(args ...string) (string, error) {
		out, err := exec.Command(docker, args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("network", "create", id); err != nil {
		t.Fatalf("network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = run("rm", "-fv", id+"-addon", id+"-store")
		_, _ = run("network", "rm", id)
	})
	for _, c := range []string{"addon", "store"} {
		if out, err := run("run", "-d", "--name", id+"-"+c, "--network", id,
			"-e", "MINIO_ROOT_USER=rootuser"+c, "-e", "MINIO_ROOT_PASSWORD=rootpassword"+c,
			minioImage, "server", "/data"); err != nil {
			t.Fatalf("start minio %s: %v\n%s", c, err, out)
		}
	}
	mc := func(sh string) string {
		t.Helper()
		full := `set -e
for i in $(seq 1 30); do mc alias set addon http://` + id + `-addon:9000 rootuseraddon rootpasswordaddon >/dev/null 2>&1 && break; sleep 1; done
for i in $(seq 1 30); do mc alias set store http://` + id + `-store:9000 rootuserstore rootpasswordstore >/dev/null 2>&1 && break; sleep 1; done
` + sh
		out, err := run("run", "--rm", "--network", id, "-e", "HOME=/tmp", "--entrypoint", "sh", mcImage, "-c", full)
		if err != nil {
			t.Fatalf("mc: %v\n%s", err, out)
		}
		return out
	}
	mc(`mc mb addon/files addon/extra store/backups >/dev/null
printf 'TOP-SECRET-INVOICE' | mc pipe addon/files/docs/invoice.pdf >/dev/null
printf 'EXTRA-BUCKET-BODY' | mc pipe addon/extra/e.txt >/dev/null`)

	const key = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZab"
	backupEnv := func(withKey bool) []string {
		env := []string{
			"BUCKET=backups", "S3_ENDPOINT=http://" + id + "-store:9000",
			"AWS_ACCESS_KEY_ID=rootuserstore", "AWS_SECRET_ACCESS_KEY=rootpasswordstore", "AWS_DEFAULT_REGION=us-east-1",
			"PROJECT=alpha", "ADDON=alpha-files", "RETENTION_DAYS=0",
			"SRC_BUCKET=files", "SRC_ENDPOINT=http://" + id + "-addon:9000",
			"SRC_ACCESS_KEY=rootuseraddon", "SRC_SECRET_KEY=rootpasswordaddon",
		}
		if withKey {
			env = append(env, "BACKUP_ENCRYPTION_KEY="+key)
		}
		return env
	}
	inImage := func(sh string, env []string) (string, error) {
		args := []string{"run", "--rm", "--network", id}
		for _, e := range env {
			args = append(args, "-e", e)
		}
		return run(append(args, "--entrypoint", "sh", backupImage, "-c", sh)...)
	}
	snapshot := func(withKey bool) string {
		t.Helper()
		before := mc(`mc ls store/backups/alpha/alpha-files/ 2>/dev/null || true`)
		time.Sleep(1100 * time.Millisecond) // distinct second-granularity stamps
		if out, err := inImage(script, backupEnv(withKey)); err != nil {
			t.Fatalf("backup run: %v\n%s", err, out)
		}
		after := mc(`mc ls store/backups/alpha/alpha-files/`)
		for _, line := range strings.Split(after, "\n") {
			f := strings.Fields(line)
			if len(f) == 0 {
				continue
			}
			dir := strings.TrimSuffix(f[len(f)-1], "/")
			if !strings.Contains(before, dir) && !strings.Contains(dir, ".bucket-") {
				return dir
			}
		}
		t.Fatalf("no new snapshot:\n%s", after)
		return ""
	}
	restore := func(key, tgt, encKey string, extra ...string) (string, error) {
		env := []string{
			"KEY=" + key, "BUCKET=backups", "S3_ENDPOINT=http://" + id + "-store:9000",
			"AWS_ACCESS_KEY_ID=rootuserstore", "AWS_SECRET_ACCESS_KEY=rootpasswordstore", "AWS_DEFAULT_REGION=us-east-1",
			"TGT_BUCKET=" + tgt, "TGT_ENDPOINT=http://" + id + "-addon:9000",
			"TGT_ACCESS_KEY=rootuseraddon", "TGT_SECRET_KEY=rootpasswordaddon",
		}
		if encKey != "" {
			env = append(env, "BACKUP_ENCRYPTION_KEY="+encKey)
		}
		return inImage(backup.S3SnapshotRestoreScript, append(env, extra...))
	}

	enc := snapshot(true)
	stored := mc(`mc cat store/backups/alpha/alpha-files/` + enc + `/docs/invoice.pdf.bin; echo; mc cat store/backups/alpha/alpha-files/` + enc + `/.kuso-snapshot.manifest.json`)
	if strings.Contains(stored, "TOP-SECRET-INVOICE") {
		t.Fatalf("encrypted snapshot holds plaintext:\n%s", stored)
	}
	if !strings.Contains(stored, `"encryption"`) {
		t.Fatalf("snapshot manifest does not record encryption:\n%s", stored)
	}
	objKey := "alpha/alpha-files/" + enc + "/docs/invoice.pdf.bin"
	if out, err := restore(objKey, "restored", ""); err == nil {
		t.Fatalf("restore of an encrypted snapshot without the key succeeded:\n%s", out)
	}
	if out, err := restore(objKey, "restored", key); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got := mc(`mc cat addon/restored/docs/invoice.pdf`); !strings.Contains(got, "TOP-SECRET-INVOICE") {
		t.Errorf("restored object = %q", got)
	}
	if ls := mc(`mc ls --recursive addon/restored`); strings.Contains(ls, ".kuso-snapshot") || strings.Contains(ls, ".bin") {
		t.Errorf("restore leaked snapshot internals:\n%s", ls)
	}
	mc(`mc rb --force addon/extra >/dev/null`)
	if out, err := restore("alpha/alpha-files/"+enc+".bucket-extra/e.txt.bin", "files", key); err != nil {
		t.Fatalf("extra-bucket restore: %v\n%s", err, out)
	}
	if got := mc(`mc cat addon/extra/e.txt`); !strings.Contains(got, "EXTRA-BUCKET-BODY") {
		t.Errorf("restored extra bucket object = %q", got)
	}

	// A plaintext snapshot for an addon that encrypts is refused unless
	// the caller accepts it unverified (BLOB-R2); without a key it restores.
	plain := snapshot(false)
	plainKey := "alpha/alpha-files/" + plain + "/docs/invoice.pdf"
	if out, err := restore(plainKey, "plain", key); err == nil {
		t.Fatalf("plaintext snapshot restored for an addon with a backup key:\n%s", out)
	}
	if out, err := restore(plainKey, "plain", key, "ALLOW_UNVERIFIED=1"); err != nil {
		t.Fatalf("plaintext restore with allowUnverified: %v\n%s", err, out)
	}
	if out, err := restore(plainKey, "plain", ""); err != nil {
		t.Fatalf("plaintext restore without a key: %v\n%s", err, out)
	}
	if got := mc(`mc cat addon/plain/docs/invoice.pdf`); !strings.Contains(got, "TOP-SECRET-INVOICE") {
		t.Errorf("plaintext restored object = %q", got)
	}
}
