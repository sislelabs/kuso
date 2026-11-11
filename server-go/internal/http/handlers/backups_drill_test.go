package handlers

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kuso/server/internal/kube"
)

func TestParseDrillLog(t *testing.T) {
	t.Parallel()
	st := DrillStatus{Phase: "Succeeded"}
	parseDrillLog("==> restoring k\n==> done\nDRILL_RESULT tables=4 rows=1200\n", &st)
	if st.Tables == nil || *st.Tables != 4 || *st.Rows != 1200 || st.Message != "" {
		t.Errorf("success: %+v", st)
	}
	st = DrillStatus{Phase: "Failed"}
	parseDrillLog("DRILL_UNSUPPORTED: role app cannot CREATE DATABASE, so there is nowhere\n", &st)
	if st.Tables != nil || !strings.Contains(st.Message, "cannot CREATE DATABASE") {
		t.Errorf("unsupported: %+v", st)
	}
	st = DrillStatus{Phase: "Failed", Job: "j"}
	parseDrillLog("==> checksum MISMATCH: manifest=a actual=b — aborting\n", &st)
	if !strings.Contains(st.Message, "MISMATCH") {
		t.Errorf("mismatch: %+v", st)
	}
}

func TestNewestPrimaryDump(t *testing.T) {
	t.Parallel()
	items := []BackupObject{
		{Key: "p/a/20261102T030000Z.db-other.sql.gz"},
		{Key: "p/a/20261101T030000Z.sql.gz"},
		{Key: "p/a/20261031T030000Z.sql.gz"},
	}
	if got := newestPrimaryDump(items); got != "p/a/20261101T030000Z.sql.gz" {
		t.Errorf("got %q", got)
	}
}

func TestAddonVolumeBytes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		spec kube.KusoAddonSpec
		want int64
	}{
		{kube.KusoAddonSpec{}, 5 << 30},
		{kube.KusoAddonSpec{Size: "medium"}, 20 << 30},
		{kube.KusoAddonSpec{Size: "small", StorageSize: "50Gi"}, 50 << 30},
	} {
		if got := addonVolumeBytes(&kube.KusoAddon{Spec: tc.spec}); got != tc.want {
			t.Errorf("%+v: %d, want %d", tc.spec, got, tc.want)
		}
	}
}

// The drill, run for real in the backup image against a real postgres:
// the dump restores into a scratch database through the production
// restore script, counts come back, the scratch database is dropped and
// the live one is untouched; a role without CREATEDB is refused.
func TestRestoreDrill_RoundTrip_Docker(t *testing.T) {
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
	id := fmt.Sprintf("kuso-drill-%d", time.Now().UnixNano())
	if out, err := run("network", "create", id); err != nil {
		t.Fatalf("network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_, _ = run("rm", "-f", id+"-pg")
		_, _ = run("network", "rm", id)
	})
	if out, err := run("run", "-d", "--name", id+"-pg", "--network", id,
		"-e", "POSTGRES_USER=kuso", "-e", "POSTGRES_PASSWORD=pw", "-e", "POSTGRES_DB=app", "postgres:16-alpine"); err != nil {
		t.Fatalf("start postgres: %v\n%s", err, out)
	}

	script, err := drillScript()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drill.sh"), []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	setup := `set -e
export PGPASSWORD=pw
for i in $(seq 1 60); do pg_isready -h "$H" -U kuso -q && psql -h "$H" -U kuso -d app -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
psql -q -h "$H" -U kuso -d app -c "CREATE TABLE IF NOT EXISTS t (id int); TRUNCATE t; INSERT INTO t SELECT generate_series(1,3);"
psql -q -h "$H" -U kuso -d app -c "DO \$\$BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='weak') THEN CREATE ROLE weak LOGIN PASSWORD 'pw'; END IF; END\$\$;"
mkdir -p /tmp/fx /tmp/bin
/usr/libexec/postgresql16/pg_dump --clean --if-exists -h "$H" -U kuso app | gzip > /tmp/fx/dump.sql.gz
printf '{"artifacts":[{"sha256":"%s"}]}' "$(sha256sum /tmp/fx/dump.sql.gz | cut -d' ' -f1)" > /tmp/fx/manifest.json
printf '#!/bin/sh\ncase "$5" in *.manifest.json) cp /tmp/fx/manifest.json "$6" ;; *) cp /tmp/fx/dump.sql.gz "$6" ;; esac\n' > /tmp/bin/aws
chmod +x /tmp/bin/aws
export PATH=/tmp/bin:$PATH
export POSTGRES_HOST="$H" POSTGRES_DB=app POSTGRES_PASSWORD=pw BUCKET=b S3_ENDPOINT=http://s3 KEY=p/a/20261101T030000Z.sql.gz
export DRILL_DB=kuso_drill_1 DRILL_SIZE_DB=app DRILL_VOLUME_BYTES=0
set +e
POSTGRES_USER=weak sh /fx/drill.sh; echo "WEAK_RC=$?"
POSTGRES_USER=kuso sh /fx/drill.sh; echo "DRILL_RC=$?"
DRILL_VOLUME_BYTES=1 POSTGRES_USER=kuso sh /fx/drill.sh; echo "FULL_RC=$?"
# appRole: the dump replays as the conn role, never the admin that made
# the scratch database; the guard fails any replay run as superuser.
{ /usr/libexec/postgresql16/pg_dump --clean --if-exists --no-owner -h "$H" -U kuso app
  echo "DO \$\$BEGIN IF (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN RAISE EXCEPTION 'replayed as superuser'; END IF; END\$\$;"
} | gzip > /tmp/fx/dump.sql.gz
printf '{"artifacts":[{"sha256":"%s"}]}' "$(sha256sum /tmp/fx/dump.sql.gz | cut -d' ' -f1)" > /tmp/fx/manifest.json
DRILL_ADMIN_USER=kuso DRILL_ADMIN_PASSWORD=pw POSTGRES_USER=weak sh /fx/drill.sh; echo "APPROLE_RC=$?"
POSTGRES_USER=kuso sh /fx/drill.sh; echo "SUPER_RC=$?"
echo "LEFT=$(psql -qAt -h "$H" -U kuso -d app -c "select count(*) from pg_database where datname like 'kuso_drill_%'")"
echo "LIVE=$(psql -qAt -h "$H" -U kuso -d app -c "select count(*) from t")"
`
	ensure := exec.Command(docker, "image", "inspect", backupJobImage)
	if ensure.Run() != nil {
		if out, err := run("pull", "-q", "--platform", "linux/amd64", backupJobImage); err != nil {
			t.Fatalf("pull: %v\n%s", err, out)
		}
	}
	out, err := run("run", "--rm", "--platform", "linux/amd64", "--network", id, "-v", dir+":/fx:ro",
		"-e", "H="+id+"-pg", "--entrypoint", "sh", backupJobImage, "-c", setup)
	if err != nil {
		t.Fatalf("drill run: %v\n%s", err, out)
	}
	for _, want := range []string{
		"DRILL_UNSUPPORTED: role weak cannot CREATE DATABASE", "WEAK_RC=2",
		"DRILL_RESULT tables=1 rows=3", "DRILL_RC=0",
		"would push the addon volume past 85%", "FULL_RC=2",
		"APPROLE_RC=0", "replayed as superuser",
		"LEFT=0", "LIVE=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SUPER_RC=0") {
		t.Errorf("the superuser guard never fired, so APPROLE_RC proves nothing:\n%s", out)
	}
}
