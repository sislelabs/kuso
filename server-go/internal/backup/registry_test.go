package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kuso/server/internal/testsh"
)

func TestRegistryResolvesKnownKinds(t *testing.T) {
	r := NewDefaultRegistry()
	for kind, wantPayload := range map[string]string{
		"postgres": "pg_dump",
		"redis":    "redis_rdb",
		"mongodb":  "mongodump",
	} {
		p, ok := r.For(kind)
		if !ok {
			t.Fatalf("For(%q) not found", kind)
		}
		if p.PayloadKind() != wantPayload {
			t.Errorf("For(%q).PayloadKind() = %q, want %q", kind, p.PayloadKind(), wantPayload)
		}
	}
}

func TestRegistryUnknownKind(t *testing.T) {
	r := NewDefaultRegistry()
	if _, ok := r.For("nats"); ok {
		t.Error("nats should not be backable yet")
	}
}

func TestPostgresRestoreScriptUnchangedContract(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	for _, want := range []string{"gunzip -c /tmp/dump.sql.gz", "psql", "manifest.json", "sha256sum", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("postgres restore script missing %q", want)
		}
	}
}

func TestMongoRestoreScript(t *testing.T) {
	p, _ := NewDefaultRegistry().For("mongodb")
	s := p.RestoreScript()
	for _, want := range []string{"mongorestore", "--archive", "--gzip", "MONGO_URL", "manifest.json", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("mongo restore script missing %q", want)
		}
	}
}

// TestPostgresRestoreIsAtomicAndFailLoud pins the P1 fix: the restore must
// pipe into psql with ON_ERROR_STOP=1 (no silent partial apply) and
// --single-transaction (all-or-nothing). Without these a broken/incompatible
// dump onto a populated DB would silently no-op or duplicate rows.
func TestPostgresRestoreIsAtomicAndFailLoud(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	for _, want := range []string{"ON_ERROR_STOP=1", "--single-transaction"} {
		if !strings.Contains(s, want) {
			t.Errorf("postgres restore script missing %q — silent-partial guard not in place", want)
		}
	}
	// The apply must still gunzip the artifact and target the addon's DB.
	if !strings.Contains(s, `gunzip -c /tmp/dump.sql.gz`) || !strings.Contains(s, `"${POSTGRES_DB}"`) {
		t.Errorf("postgres restore apply line malformed: %q", s)
	}
}

func TestMysqlProducer(t *testing.T) {
	p, ok := NewDefaultRegistry().For("mysql")
	if !ok {
		t.Fatal("mysql not registered")
	}
	if p.PayloadKind() != "mysqldump" || p.ArtifactExt() != "sql.gz" {
		t.Fatalf("mysql producer metadata wrong: %s/%s", p.PayloadKind(), p.ArtifactExt())
	}
	s := p.RestoreScript()
	for _, want := range []string{"mysql", "MYSQL_HOST", "manifest.json", "MISMATCH", "gunzip"} {
		if !strings.Contains(s, want) {
			t.Errorf("mysql restore script missing %q", want)
		}
	}
}

// A fresh restore pod in a project namespace is network-isolated for
// ~5-20s until kube-router syncs the netpol for its IP, and the Job runs
// with BackoffLimit 0. Every restore kind must retry the artifact download,
// and postgres must wait for the server before psql.
func TestRestoreScripts_RetryDownloadBeforeFirstUse(t *testing.T) {
	r := NewDefaultRegistry()
	for _, kind := range []string{"postgres", "redis", "mongodb", "mysql"} {
		p, _ := r.For(kind)
		s := p.RestoreScript()
		def := strings.Index(s, "s3_fetch() {")
		if def < 0 {
			t.Errorf("%s: no s3_fetch retry helper", kind)
			continue
		}
		if !strings.Contains(s[def:], "until aws s3 cp") {
			t.Errorf("%s: s3_fetch does not retry aws s3 cp", kind)
		}
		if first := strings.Index(s, `aws s3 cp --endpoint-url "${S3_ENDPOINT}" "s3://${BUCKET}/${KEY}" `); first >= 0 {
			t.Errorf("%s: artifact downloaded with a bare aws s3 cp (no retry) at %d", kind, first)
		}
		if !strings.Contains(s, `s3_fetch "s3://${BUCKET}/${KEY}" /tmp/dump.`) {
			t.Errorf("%s: artifact download does not go through s3_fetch", kind)
		}
	}
}

func TestPostgresRestore_WaitsForServerBeforePsql(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	wait := strings.Index(s, `pg_isready -h "${POSTGRES_HOST}"`)
	apply := strings.Index(s, `psql -v ON_ERROR_STOP=1`)
	if wait < 0 || apply < 0 || wait > apply {
		t.Fatalf("pg_isready wait (at %d) must precede the psql apply (at %d)", wait, apply)
	}
	if !strings.Contains(s[:apply], "exit 1") {
		t.Error("wait loop is not bounded (no exit 1 before the apply)")
	}
}

// The download proves egress works, not that the database's ingress policy
// has admitted the fresh restore pod. mongo and mysql went straight from the
// download to mongorestore/mysql, so a restore racing kube-router failed on
// the first refused connect (BackoffLimit 0). They must wait, bounded.
func TestMongoMysqlRestore_WaitForServerBeforeApply(t *testing.T) {
	cases := []struct {
		kind, env, apply, wantNC string
	}{
		{"mongodb", "MONGO_URL=mongodb://u:p@mongo-host:27018/app?authSource=admin", "mongorestore", "mongo-host 27018"},
		{"mysql", "MYSQL_HOST=mysql-host", "mysql", "mysql-host 3306"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.kind, func(t *testing.T) {
			p, _ := NewDefaultRegistry().For(tc.kind)
			s := p.RestoreScript()
			if out, err := testsh.Command(t, s, "-n").CombinedOutput(); err != nil {
				t.Fatalf("restore script does not parse: %v\n%s", err, out)
			}
			wait := strings.Index(s, "nc -z")
			apply := strings.LastIndex(s, tc.apply+" ")
			if wait < 0 || apply < 0 || wait > apply {
				t.Fatalf("nc -z wait (at %d) must precede %s (at %d)", wait, tc.apply, apply)
			}

			out, applied, ncArgs, err := runRestoreScript(t, s, tc.apply, "exit 0", tc.env)
			if err != nil || !applied {
				t.Fatalf("reachable server: applied=%v err=%v\n%s", applied, err, out)
			}
			if !strings.Contains(ncArgs, tc.wantNC) {
				t.Errorf("nc probed %q, want host/port %q", ncArgs, tc.wantNC)
			}

			out, applied, _, err = runRestoreScript(t, s, tc.apply, "exit 1", tc.env)
			if applied || err == nil {
				t.Errorf("unreachable server: applied=%v err=%v\n%s", applied, err, out)
			}
			if !strings.Contains(out, "unreachable after 60s") {
				t.Errorf("output missing unreachable message:\n%s", out)
			}
		})
	}
}

// runRestoreScript runs a restore script with stubbed aws/nc/sleep/gunzip
// and a stubbed apply binary that records whether it ran. /tmp paths are
// redirected into a temp dir so runs don't share files.
func runRestoreScript(t *testing.T, script, apply, ncBody, connEnv string) (string, bool, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "applied")
	ncLog := filepath.Join(dir, "nc.log")
	stubs := map[string]string{
		// aws s3 cp --endpoint-url E src dst: no manifest, artifact "downloads".
		"aws":    `case "$5" in *.manifest.json) exit 1;; esac; : > "$6"`,
		"nc":     `echo "$@" >> ` + ncLog + "\n" + ncBody,
		"sleep":  "exit 0",
		"gunzip": "echo 'select 1;'",
		apply:    "cat > /dev/null; touch " + marker,
	}
	for name, body := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script = strings.ReplaceAll(script, "/tmp/", dir+"/")
	cmd := testsh.Command(t, script)
	cmd.Env = []string{
		"PATH=" + bin + ":" + os.Getenv("PATH"),
		"BUCKET=b", "S3_ENDPOINT=e", "KEY=p/a/k", connEnv,
		"MYSQL_USER=u", "MYSQL_DB=d", "MYSQL_PASSWORD=pw",
	}
	out, err := cmd.CombinedOutput()
	_, statErr := os.Stat(marker)
	nc, _ := os.ReadFile(ncLog)
	return string(out), statErr == nil, string(nc), err
}
