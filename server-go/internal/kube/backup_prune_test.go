package kube

import (
	"bytes"
	"fmt"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"
)

// backupImage is the image the rendered CronJob runs. The prune shell has
// to be exercised in it, not on the host: the retention bug was that the
// image's busybox `date` could not parse what GNU date parses, so every
// object was skipped while the Job still exited 0.
const backupImage = "ghcr.io/sislelabs/kuso-backup:latest"

const (
	pruneBegin = "# retention-prune:begin"
	pruneEnd   = "# retention-prune:end"
)

// renderedPruneBlock renders the backup CronJob for one addon variant and
// returns the retention-prune shell exactly as the Job would run it.
func renderedPruneBlock(t *testing.T, kind string, sets ...string) string {
	t.Helper()
	sets = append([]string{"backup.schedule=0 3 * * *", "backup.retentionDays=30"}, sets...)
	out := helmTemplateAddon(t, kind, sets...)
	start := strings.Index(out, pruneBegin)
	end := strings.Index(out, pruneEnd)
	if start < 0 || end < start {
		t.Fatalf("kind=%s: prune markers not found in rendered chart", kind)
	}
	return out[start : end+len(pruneEnd)]
}

func requireBackupImage(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: skipping docker-backed prune test")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		t.Skip("docker not found; skipping prune test")
	}
	if exec.Command(docker, "image", "inspect", backupImage).Run() != nil {
		if out, err := exec.Command(docker, "pull", "-q", "--platform", "linux/amd64", backupImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull %s: %v\n%s", backupImage, err, out)
		}
	}
	return docker
}

type pruneRun struct {
	deleted []string
	stdout  string
}

// runPrune executes the rendered prune block inside the backup image with a
// fake `aws` on PATH. `aws s3 ls` prints listing and exits lsRC; `aws s3 rm`
// records its target instead of deleting anything.
func runPrune(t *testing.T, docker, block, listing string, lsRC int) pruneRun {
	t.Helper()
	script := fmt.Sprintf(`set -euo pipefail
mkdir -p /tmp/bin
cat > /tmp/listing <<'LISTING_EOF'
%s
LISTING_EOF
cat > /tmp/bin/aws <<'AWS_EOF'
#!/bin/sh
case "$2" in
  ls) cat /tmp/listing; exit %d ;;
  rm) for a in "$@"; do case "$a" in s3://*) echo "$a" >> /tmp/rm.log ;; esac; done ;;
esac
AWS_EOF
chmod +x /tmp/bin/aws
export PATH=/tmp/bin:$PATH
BUCKET=bkt S3_ENDPOINT=http://fake PREFIX=alpha/db/ RETENTION_DAYS=30
%s
echo "==> done"
echo "---RM---"
cat /tmp/rm.log 2>/dev/null || true
`, listing, lsRC, block)

	cmd := exec.Command(docker, "run", "--rm", "-i", "--platform", "linux/amd64",
		"--entrypoint", "sh", backupImage, "-s")
	cmd.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("prune script failed (a prune must never fail the Job): %v\n%s", err, out.String())
	}
	body, rmLog, _ := strings.Cut(out.String(), "---RM---\n")
	if !strings.Contains(body, "==> done") {
		t.Fatalf("script did not reach done:\n%s", out.String())
	}
	var deleted []string
	for _, l := range strings.Split(strings.TrimSpace(rmLog), "\n") {
		if l != "" {
			deleted = append(deleted, strings.TrimPrefix(l, "s3://bkt/alpha/db/"))
		}
	}
	sort.Strings(deleted)
	return pruneRun{deleted: deleted, stdout: body}
}

func stampDaysAgo(now time.Time, days int) string {
	return now.AddDate(0, 0, -days).Format("20060102T150405Z")
}

func fileLine(name string) string {
	return "2026-01-01 00:00:00       1234 " + name
}

func prefixLine(stamp string) string {
	return "                           PRE " + stamp + "/"
}

func assertDeleted(t *testing.T, r pruneRun, want []string) {
	t.Helper()
	sort.Strings(want)
	if strings.Join(r.deleted, "\n") != strings.Join(want, "\n") {
		t.Fatalf("deleted mismatch\n got: %q\nwant: %q\noutput:\n%s", r.deleted, want, r.stdout)
	}
}

// fileKinds are the variants whose backups are timestamp-named objects
// (<ts>.sql.gz and friends); s3 backs up to <ts>/ prefixes instead.
var fileKinds = []struct {
	name string
	kind string
	sets []string
}{
	{"postgres", "postgres", nil},
	{"postgres-external", "postgres", []string{"external=true"}},
	{"redis", "redis", nil},
	{"mongodb", "mongodb", nil},
	{"mysql", "mysql", nil},
}

func TestBackupPrune_DeletesOnlyExpiredObjects(t *testing.T) {
	docker := requireBackupImage(t)
	now := time.Now().UTC()

	var listing []string
	var want []string
	for _, days := range []int{60, 45, 31, 29, 20, 10, 1, 0} {
		s := stampDaysAgo(now, days)
		names := []string{s + ".sql.gz", s + ".sql.gz.manifest.json"}
		if days == 60 {
			names = append(names, s+".db-tenant.sql.gz")
		}
		for _, n := range names {
			listing = append(listing, fileLine(n))
			if days > 30 {
				want = append(want, n)
			}
		}
	}
	// Names without a leading timestamp are never ours to delete.
	listing = append(listing,
		"2020-01-01 00:00:00         10 README.txt",
		"2020-01-01 00:00:00         10 latest.sql.gz",
		prefixLine("20200101T000000Z"))

	for _, v := range fileKinds {
		t.Run(v.name, func(t *testing.T) {
			block := renderedPruneBlock(t, v.kind, v.sets...)
			r := runPrune(t, docker, block, strings.Join(listing, "\n"), 0)
			assertDeleted(t, r, want)
		})
	}
}

func TestBackupPrune_S3SnapshotPrefixes(t *testing.T) {
	docker := requireBackupImage(t)
	now := time.Now().UTC()
	block := renderedPruneBlock(t, "s3")

	var listing, want []string
	for _, days := range []int{90, 40, 31, 29, 20, 5, 0} {
		s := stampDaysAgo(now, days)
		listing = append(listing, prefixLine(s))
		if days > 30 {
			want = append(want, s+"/")
		}
	}
	listing = append(listing, prefixLine("manual-copy"),
		fileLine(stampDaysAgo(now, 90)+".stray"))

	r := runPrune(t, docker, block, strings.Join(listing, "\n"), 0)
	assertDeleted(t, r, want)
}

// If every backup is past retention (the job has been failing for weeks),
// the newest three must survive: expiring the last good copy is worse than
// keeping a stale one.
func TestBackupPrune_KeepsNewestThreeEvenWhenAllExpired(t *testing.T) {
	docker := requireBackupImage(t)
	now := time.Now().UTC()

	t.Run("files", func(t *testing.T) {
		block := renderedPruneBlock(t, "postgres")
		var listing, want []string
		for _, days := range []int{120, 90, 60, 40, 35} {
			s := stampDaysAgo(now, days)
			for _, n := range []string{s + ".sql.gz", s + ".sql.gz.manifest.json"} {
				listing = append(listing, fileLine(n))
				if days >= 90 {
					want = append(want, n)
				}
			}
		}
		assertDeleted(t, runPrune(t, docker, block, strings.Join(listing, "\n"), 0), want)
	})

	t.Run("two_backups_only", func(t *testing.T) {
		block := renderedPruneBlock(t, "postgres")
		listing := []string{
			fileLine(stampDaysAgo(now, 200) + ".sql.gz"),
			fileLine(stampDaysAgo(now, 100) + ".sql.gz"),
		}
		assertDeleted(t, runPrune(t, docker, block, strings.Join(listing, "\n"), 0), nil)
	})

	t.Run("s3", func(t *testing.T) {
		block := renderedPruneBlock(t, "s3")
		var listing, want []string
		for _, days := range []int{100, 80, 60, 50} {
			s := stampDaysAgo(now, days)
			listing = append(listing, prefixLine(s))
			if days == 100 {
				want = append(want, s+"/")
			}
		}
		assertDeleted(t, runPrune(t, docker, block, strings.Join(listing, "\n"), 0), want)
	})
}

// Anything short of a clean listing must delete nothing.
func TestBackupPrune_FailsSafe(t *testing.T) {
	docker := requireBackupImage(t)
	now := time.Now().UTC()
	var old []string
	for _, days := range []int{300, 200, 100, 90, 80} {
		s := stampDaysAgo(now, days)
		old = append(old, fileLine(s+".sql.gz"), prefixLine(s))
	}

	cases := []struct {
		name    string
		listing string
		rc      int
	}{
		{"empty_listing", "", 0},
		{"garbage_listing", "An error occurred (AccessDenied)\n<html>nope</html>\n\n", 0},
		{"listing_command_failed_after_partial_output", strings.Join(old, "\n"), 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, kind := range []string{"postgres", "s3"} {
				block := renderedPruneBlock(t, kind)
				assertDeleted(t, runPrune(t, docker, block, c.listing, c.rc), nil)
			}
		})
	}
}
