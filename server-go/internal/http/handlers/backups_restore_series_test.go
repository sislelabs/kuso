package handlers

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotSeries(t *testing.T) {
	for key, want := range map[string]string{
		"shop/shop-db/20261110T030000Z.sql.gz":           ".sql.gz.manifest.json",
		"shop/shop-db/20261110T030000Z.db-orders.sql.gz": ".db-orders.sql.gz.manifest.json",
		"shop/shop-mongo/20261110T030000Z.archive.gz":    ".archive.gz.manifest.json",
		"shop/shop-db/legacy.sql.gz":                     "",
	} {
		if got := snapshotSeries(key); got != want {
			t.Errorf("snapshotSeries(%q) = %q, want %q", key, got, want)
		}
	}
}

// BLOB-4: restoring a per-database dump waits for THAT database's
// pre-restore dump, not the primary's, which lands first.
func TestSnapshotWaitShell_WaitsForTheRestoredSeries(t *testing.T) {
	t.Parallel()
	since := time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)
	key := "shop/shop-db/20261110T030000Z.db-orders.sql.gz"
	c := snapshotWaitContainer("kuso-backup-s3", "", "shop/shop-db/", since, snapshotSeries(key), "orders", "shop-db-conn")
	primaryOnly := "2026-11-11 10:00:06   1234 20261111T100001Z.sql.gz.manifest.json\n2026-11-11 10:00:07   99 20261111T100001Z.db-orders.sql.gz"
	withOrders := primaryOnly + "\n2026-11-11 10:00:09   1234 20261111T100001Z.db-orders.sql.gz.manifest.json"
	for _, tc := range []struct {
		name    string
		listing string
		psql    string
		ok      bool
	}{
		{"db exists, only primary saved", primaryOnly, "echo 1", false},
		{"db exists, its dump saved", withOrders, "echo 1", true},
		{"db does not exist yet", primaryOnly, "exit 0", true},
		{"existence unknown", primaryOnly, "exit 2", false},
		{"another db's dump does not count", primaryOnly + "\n2026-11-11 10:00:09   1234 20261111T100001Z.db-ordersx.sql.gz.manifest.json", "echo 1", false},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "ls"), []byte(tc.listing+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, body := range map[string]string{
			"aws":   "cat " + filepath.Join(dir, "ls"),
			"psql":  "cat >/dev/null; " + tc.psql,
			"sleep": "exit 0",
		} {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command("sh", "-c", c.Args[0])
		cmd.Env = []string{"PATH=" + dir + ":/usr/bin:/bin", "BUCKET=b", "S3_ENDPOINT=http://x",
			"POSTGRES_HOST=h", "POSTGRES_USER=u", "POSTGRES_DB=shop", "POSTGRES_PASSWORD=p"}
		for _, e := range c.Env {
			if e.ValueFrom == nil {
				cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
			}
		}
		cmd.Env = append(cmd.Env, "SNAP_WAIT_SECONDS=0")
		out, err := cmd.CombinedOutput()
		if (err == nil) != tc.ok {
			t.Errorf("%s: err=%v, want ok=%v\n%s", tc.name, err, tc.ok, out)
		}
	}
}
