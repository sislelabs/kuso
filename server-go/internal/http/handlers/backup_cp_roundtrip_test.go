package handlers

import (
	"bytes"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"kuso/server/internal/db"
)

// cronJobDumpScript returns the dump initContainer's shell from
// deploy/postgres-backup.yaml, exactly as the CronJob runs it.
func cronJobDumpScript(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "deploy", "postgres-backup.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var cj struct {
		Spec struct {
			JobTemplate struct {
				Spec struct {
					Template struct {
						Spec struct {
							InitContainers []struct {
								Name  string   `yaml:"name"`
								Image string   `yaml:"image"`
								Args  []string `yaml:"args"`
							} `yaml:"initContainers"`
						} `yaml:"spec"`
					} `yaml:"template"`
				} `yaml:"spec"`
			} `yaml:"jobTemplate"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal(raw, &cj); err != nil {
		t.Fatalf("decode postgres-backup.yaml: %v", err)
	}
	for _, c := range cj.Spec.JobTemplate.Spec.Template.Spec.InitContainers {
		if c.Name == "dump" {
			return c.Args[0]
		}
	}
	t.Fatal("no dump initContainer in deploy/postgres-backup.yaml")
	return ""
}

// The scheduled control-plane backup must come back through the product's
// restore path (DB-4) and leave the cluster encrypted (DB-5): the CronJob's
// own dump script runs against a booted kuso schema, its encrypted output is
// decrypted the way Upload does, and the restore script loads it over a live
// database. A dump from before --clean (the old CronJob flags) must load too.
func TestControlPlaneBackupRestore_Docker(t *testing.T) {
	if os.Getenv("KUSO_TEST_DOCKER") == "" {
		t.Skip("docker-backed test: set KUSO_TEST_DOCKER=1 (make test-docker) to run it")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal("KUSO_TEST_DOCKER is set but docker is not on PATH")
	}
	run := func(args ...string) (string, error) {
		out, err := exec.Command(docker, args...).CombinedOutput()
		return string(out), err
	}
	id := fmt.Sprintf("kuso-cprt-%d", time.Now().UnixNano())
	if out, err := run("network", "create", id); err != nil {
		t.Fatalf("network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = run("rm", "-fv", id+"-pg")
		_, _ = run("network", "rm", id)
	})
	if out, err := run("run", "-d", "--name", id+"-pg", "--network", id, "-p", "127.0.0.1::5432",
		"-e", "POSTGRES_USER=kuso", "-e", "POSTGRES_PASSWORD=pw", "-e", "POSTGRES_DB=kuso", "postgres:16-alpine"); err != nil {
		t.Fatalf("start postgres: %v\n%s", err, out)
	}
	portOut, err := run("port", id+"-pg", "5432/tcp")
	if err != nil {
		t.Fatalf("port: %v\n%s", err, portOut)
	}
	hostDSN := "postgres://kuso:pw@" + strings.TrimSpace(strings.Split(portOut, "\n")[0]) + "/kuso?sslmode=disable"
	netDSN := "postgres://kuso:pw@" + id + "-pg:5432/kuso?sslmode=disable"

	var database *db.DB
	deadline := time.Now().Add(2 * time.Minute)
	for {
		if database, err = db.Open(hostDSN); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("boot kuso schema: %v", err)
		}
		time.Sleep(2 * time.Second)
	}
	defer database.Close()
	psql := func(q string) string {
		t.Helper()
		out, err := run("exec", id+"-pg", "psql", "-qAt", "-U", "kuso", "-d", "kuso", "-c", q)
		if err != nil {
			t.Fatalf("psql %q: %v\n%s", q, err, out)
		}
		return strings.TrimSpace(out)
	}
	psql(`INSERT INTO "EnvHint" ("project","service","name","lastSeen") VALUES ('p','s','BEFORE',now())`)

	const key = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	dump := func(script, name string) []byte {
		t.Helper()
		out, err := run("run", "--rm", "--network", id, "-u", "999", "-v", dir+":/backup",
			"-e", "KUSO_DB_DSN="+netDSN, "-e", "KUSO_BACKUP_BUCKET=b", "-e", "KUSO_BACKUP_ENDPOINT=http://s3",
			"-e", "AWS_ACCESS_KEY_ID=a", "-e", "AWS_SECRET_ACCESS_KEY=s", "-e", "BACKUP_ENCRYPTION_KEY="+key,
			"--entrypoint", "bash", "postgres:16-bookworm", "-c", script+"\nmv \"$DUMP\" /backup/"+name)
		if err != nil {
			t.Fatalf("dump: %v\n%s", err, out)
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	script := cronJobDumpScript(t)
	cleanFlag := regexp.MustCompile(`(?m)^\s*--clean --if-exists \\\n`)
	if !cleanFlag.MatchString(script) {
		t.Fatal("CronJob dump no longer passes --clean --if-exists on its own line")
	}
	encrypted := dump(script, "new.sql.gz")
	legacy := dump(cleanFlag.ReplaceAllString(script, ""), "legacy.sql.gz")
	if bytes.Contains(encrypted, []byte("BEFORE")) || !bytes.HasPrefix(encrypted, []byte("Salted__")) {
		t.Fatal("control-plane dump left the pod unencrypted")
	}

	// Upload's decrypt path, with the key the way a DR restore passes it.
	h := &BackupHandler{}
	decrypt := func(b []byte) []byte {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/admin/restore", bytes.NewReader(b))
		req.Header.Set("X-Kuso-Backup-Key", key)
		src, status, msg := h.controlPlaneRestoreReader(req)
		if status != 0 {
			t.Fatalf("decrypt: %d %s", status, msg)
		}
		plain, err := io.ReadAll(src)
		if err != nil {
			t.Fatalf("decrypt: %v", err)
		}
		return plain
	}
	bad := httptest.NewRequest("POST", "/api/admin/restore", bytes.NewReader(encrypted))
	bad.Header.Set("X-Kuso-Backup-Key", strings.Repeat("f", 64))
	if _, status, _ := h.controlPlaneRestoreReader(bad); status == 0 {
		t.Error("a dump sealed with another key was accepted")
	}

	restore := func(gz []byte, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), gz, 0o644); err != nil {
			t.Fatal(err)
		}
		psql(`DELETE FROM "EnvHint"; INSERT INTO "EnvHint" ("project","service","name","lastSeen") VALUES ('p','s','AFTER',now())`)
		sh := strings.Replace(secretRestoreScript, "/data/dump.sql.gz", "/backup/"+name, 1)
		if out, err := run("run", "--rm", "--network", id, "-v", dir+":/backup:ro", "-e", "DSN="+netDSN,
			"--entrypoint", "sh", "postgres:16-alpine", "-ec", sh); err != nil {
			t.Fatalf("restore %s: %v\n%s", name, err, out)
		}
		if got := psql(`SELECT string_agg("name", ',') FROM "EnvHint"`); got != "BEFORE" {
			t.Errorf("%s: EnvHint after restore = %q, want BEFORE", name, got)
		}
	}
	restore(decrypt(encrypted), "plain.sql.gz")
	restore(decrypt(legacy), "legacy.sql.gz")
}
