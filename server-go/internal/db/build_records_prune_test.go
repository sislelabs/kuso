package db

import (
	"context"
	"testing"
	"time"
)

// seedBuildRecordAged inserts a BuildRecord with an explicit createdAt so
// the prune's age window can be exercised deterministically.
func seedBuildRecordAged(t *testing.T, d *DB, name, project, service, imageTag string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	if err := d.SaveBuildRecord(ctx, BuildRecord{
		BuildName: name, Project: project, Service: service,
		ImageTag: imageTag, Status: "succeeded",
	}); err != nil {
		t.Fatalf("SaveBuildRecord %s: %v", name, err)
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE "BuildRecord" SET "createdAt"=$1 WHERE "buildName"=$2`,
		time.Now().UTC().Add(-age), name); err != nil {
		t.Fatalf("age %s: %v", name, err)
	}
}

func buildRecordNames(t *testing.T, d *DB) map[string]bool {
	t.Helper()
	rows, err := d.QueryContext(context.Background(), `SELECT "buildName" FROM "BuildRecord"`)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out[n] = true
	}
	return out
}

// TestPruneBuildRecords covers the retention added 2026-08-27. BuildRecord
// was the only unbounded table on the deploy path: one row per build,
// forever, while the image-retention sweep scans the whole table once per
// namespace every 6 minutes.
//
// The prune must be conservative in two specific ways, because a wrong
// delete here loses deployment history or strands a registry image.
func TestPruneBuildRecords(t *testing.T) {
	ctx := context.Background()
	day := 24 * time.Hour

	t.Run("drops old untagged rows", func(t *testing.T) {
		d := openTestDB(t)
		seedBuildRecordAged(t, d, "old-untagged", "alpha", "web", "", 200*day)
		seedBuildRecordAged(t, d, "recent", "alpha", "web", "", 1*day)

		n, err := d.PruneBuildRecords(ctx, time.Now().Add(-90*day), 0)
		if err != nil {
			t.Fatalf("PruneBuildRecords: %v", err)
		}
		if n != 1 {
			t.Errorf("pruned %d rows, want 1", n)
		}
		got := buildRecordNames(t, d)
		if got["old-untagged"] {
			t.Error("old untagged row survived the prune")
		}
		if !got["recent"] {
			t.Error("recent row was pruned; only rows past the window may go")
		}
	})

	// A row whose imageTag is still set has NOT been untagged by the
	// image-retention sweep, so its registry image still exists and the
	// build is still rollback-able. Deleting the row would strand the
	// image with no record that it exists.
	t.Run("keeps old rows that still carry an image tag", func(t *testing.T) {
		d := openTestDB(t)
		seedBuildRecordAged(t, d, "old-tagged", "alpha", "web", "abc123", 500*day)

		n, err := d.PruneBuildRecords(ctx, time.Now().Add(-90*day), 0)
		if err != nil {
			t.Fatalf("PruneBuildRecords: %v", err)
		}
		if n != 0 {
			t.Errorf("pruned %d rows, want 0 — a tagged row is still rollback-able", n)
		}
		if !buildRecordNames(t, d)["old-tagged"] {
			t.Error("prune deleted a row whose image is still live")
		}
	})

	// A service that stopped deploying long ago must still show history,
	// or the Deployments tab goes blank for it.
	t.Run("keeps the newest N per service regardless of age", func(t *testing.T) {
		d := openTestDB(t)
		for i, name := range []string{"q1", "q2", "q3", "q4", "q5"} {
			seedBuildRecordAged(t, d, name, "alpha", "quiet", "", time.Duration(400+i)*day)
		}
		// Another service, so the PARTITION BY is actually exercised.
		seedBuildRecordAged(t, d, "other1", "alpha", "busy", "", 400*day)

		if _, err := d.PruneBuildRecords(ctx, time.Now().Add(-90*day), 2); err != nil {
			t.Fatalf("PruneBuildRecords: %v", err)
		}
		got := buildRecordNames(t, d)

		kept := 0
		for _, n := range []string{"q1", "q2", "q3", "q4", "q5"} {
			if got[n] {
				kept++
			}
		}
		if kept != 2 {
			t.Errorf("kept %d rows for the quiet service, want 2 (keepPerService)", kept)
		}
		// q1/q2 are the newest of that set (smallest age).
		if !got["q1"] || !got["q2"] {
			t.Errorf("kept the wrong rows — want the NEWEST two (q1,q2); got=%v", got)
		}
		if !got["other1"] {
			t.Error("the other service's floor row was pruned; keep is per-service, not global")
		}
	})
}
