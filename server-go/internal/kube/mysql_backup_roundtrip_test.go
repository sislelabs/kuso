package kube

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/backup"
)

// renderedCronJobScript returns the first container's shell of the backup
// CronJob the chart renders for kind.
func renderedCronJobScript(t *testing.T, kind string, sets ...string) string {
	t.Helper()
	out := helmTemplateAddon(t, kind, append([]string{"backup.schedule=0 3 * * *", "backup.retentionDays=0"}, sets...)...)
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
			t.Fatalf("decode CronJob: %v", err)
		}
		if cs := cj.Spec.JobTemplate.Spec.Template.Spec.Containers; len(cs) > 0 && len(cs[0].Args) > 0 {
			return cs[0].Args[0]
		}
	}
	t.Fatalf("no %s backup CronJob rendered", kind)
	return ""
}

// localBackupImage builds build/backup from this tree for the host arch, so
// a Dockerfile fix is proven before it is published.
func localBackupImage(t *testing.T, docker string) string {
	t.Helper()
	const ref = "kuso-backup:localtest"
	if out, err := exec.Command(docker, "build", "-q", "-t", ref, filepath.Join("..", "..", "..", "build", "backup")).CombinedOutput(); err != nil {
		t.Fatalf("build backup image: %v\n%s", err, out)
	}
	return ref
}

// fakeS3Shell puts an `aws` on PATH that maps s3://<bucket>/<key> onto
// /store, enough for the backup and restore scripts' cp calls.
const fakeS3Shell = `mkdir -p /tmp/bin /store
cat > /tmp/bin/aws <<'AWS_EOF'
#!/bin/sh
shift 2
while [ "${1#--}" != "$1" ]; do shift 2; done
src=$1 dst=$2
map() { case "$1" in s3://*) echo "/store/${1#s3://}" ;; *) echo "$1" ;; esac; }
s=$(map "$src"); d=$(map "$dst")
[ -f "$s" ] || exit 1
mkdir -p "$(dirname "$d")" && cp "$s" "$d"
AWS_EOF
chmod +x /tmp/bin/aws
export PATH=/tmp/bin:$PATH
`

// The MySQL backup CronJob and the mysql restore script, run in the backup
// image built from this tree against a real mysql:8.4 (the default version):
// the dump must connect (TLS with a self-signed cert, caching_sha2_password),
// carry routines, events and triggers, and restore the data in place (DB-1,
// DB-2).
func TestMySQLBackupRestore_Docker(t *testing.T) {
	docker := requireDocker(t)
	image := localBackupImage(t, docker)
	const mysqlImage = "mysql:8.4"
	ensureImage(t, docker, mysqlImage)
	script := renderedCronJobScript(t, "mysql")

	id := fmt.Sprintf("kuso-mysqlrt-%d", time.Now().UnixNano())
	run := func(args ...string) (string, error) {
		out, err := exec.Command(docker, args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("network", "create", id); err != nil {
		t.Fatalf("network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = run("rm", "-fv", id+"-db")
		_, _ = run("volume", "rm", "-f", id+"-store")
		_, _ = run("network", "rm", id)
	})
	if out, err := run("run", "-d", "--name", id+"-db", "--network", id,
		"-e", "MYSQL_ROOT_PASSWORD=root-only-pw", "-e", "MYSQL_USER=kuso",
		"-e", "MYSQL_PASSWORD=app-pw", "-e", "MYSQL_DATABASE=app", mysqlImage); err != nil {
		t.Fatalf("start mysql: %v\n%s", err, out)
	}
	sql := func(stmt string) string {
		t.Helper()
		out, err := run("exec", id+"-db", "mysql", "-ukuso", "-papp-pw", "-N", "-B", "app", "-e", stmt)
		if err != nil {
			t.Fatalf("mysql %q: %v\n%s", stmt, err, out)
		}
		return strings.TrimSpace(strings.ReplaceAll(out, "mysql: [Warning] Using a password on the command line interface can be insecure.", ""))
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		// The entrypoint's temporary server answers before the app user
		// exists; wait for the real one.
		if out, err := run("exec", id+"-db", "mysql", "-ukuso", "-papp-pw", "-h127.0.0.1", "app", "-e", "SELECT 1"); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("mysql never came up: %s", out)
		}
		time.Sleep(2 * time.Second)
	}
	sql(`CREATE TABLE t (id INT PRIMARY KEY, v VARCHAR(20));
INSERT INTO t VALUES (1,'one'),(2,'two'),(3,'three');
CREATE PROCEDURE p_count() SELECT COUNT(*) FROM t;
CREATE EVENT e_noop ON SCHEDULE EVERY 1 DAY DISABLE DO SELECT 1;`)

	const key = "abcdefghijklmnopqrstuvwxyz0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZab"
	env := []string{
		"BUCKET=backups", "S3_ENDPOINT=http://unused", "PROJECT=alpha", "ADDON=alpha-mysql", "RETENTION_DAYS=0",
		"MYSQL_HOST=" + id + "-db", "MYSQL_USER=kuso", "MYSQL_DB=app", "MYSQL_PASSWORD=app-pw",
		"BACKUP_ENCRYPTION_KEY=" + key,
	}
	inImage := func(sh string, extra ...string) (string, error) {
		args := []string{"run", "--rm", "--network", id, "-v", id + "-store:/store", "-u", "0"}
		for _, e := range append(env, extra...) {
			args = append(args, "-e", e)
		}
		return run(append(args, "--entrypoint", "sh", image, "-c", fakeS3Shell+sh)...)
	}
	out, err := inImage(script)
	if err != nil {
		t.Fatalf("backup run: %v\n%s", err, out)
	}
	if strings.Contains(out, "Access denied") {
		t.Errorf("dump warned about missing privileges (tablespaces need --no-tablespaces):\n%s", out)
	}
	keyOut, err := inImage(`cd /store/backups && ls alpha/alpha-mysql/*.sql.gz`)
	if err != nil {
		t.Fatalf("find dump: %v\n%s", err, keyOut)
	}
	dumpKey := strings.TrimSpace(keyOut)
	content, err := inImage(`openssl enc -d -aes-256-cbc -pbkdf2 -iter 100000 -md sha256 -pass env:BACKUP_ENCRYPTION_KEY -in /store/backups/` + dumpKey + ` | gunzip -c`)
	if err != nil {
		t.Fatalf("read dump: %v\n%s", err, content)
	}
	for _, want := range []string{"CREATE TABLE `t`", "PROCEDURE `p_count`", "EVENT `e_noop`"} {
		if !strings.Contains(content, want) {
			t.Errorf("dump lacks %q", want)
		}
	}
	// The dump's own LOCK TABLES lines are for the restore side; what keeps
	// app writes unblocked while dumping is the snapshot flag.
	if !strings.Contains(script, "--single-transaction") {
		t.Error("mysqldump runs without --single-transaction, so it locks tables for the whole dump")
	}

	sql(`DELETE FROM t WHERE id > 1; DROP PROCEDURE p_count;`)
	p, _ := backup.NewDefaultRegistry().For("mysql")
	if out, err := inImage(p.RestoreScript(), "KEY="+dumpKey); err != nil {
		t.Fatalf("restore: %v\n%s", err, out)
	}
	if got := sql(`SELECT COUNT(*) FROM t`); got != "3" {
		t.Errorf("rows after restore = %q, want 3", got)
	}
	if got := sql(`SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema='app' AND routine_name='p_count'`); got != "1" {
		t.Errorf("procedure after restore = %q, want 1", got)
	}
}
