package db

import (
	"context"
	"testing"
	"time"
)

// The build that is live right now is the one whose log you most want, and
// a service that deploys rarely used to lose it a week after every deploy.
func TestPruneBuildLogs_KeepsLiveBuilds(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"old-live", "old-dead", "recent"} {
		if err := d.SaveBuildLog(ctx, name, "alpha", "web", "succeeded", "log of "+name); err != nil {
			t.Fatalf("SaveBuildLog %s: %v", name, err)
		}
	}
	if _, err := d.ExecContext(ctx,
		`UPDATE "BuildLog" SET "createdAt"=$1 WHERE "buildName" IN ('old-live','old-dead')`,
		time.Now().UTC().Add(-60*24*time.Hour)); err != nil {
		t.Fatalf("age rows: %v", err)
	}

	n, err := d.PruneBuildLogs(ctx, time.Now().Add(-30*24*time.Hour), []string{"old-live"})
	if err != nil {
		t.Fatalf("PruneBuildLogs: %v", err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want 1", n)
	}
	for name, want := range map[string]bool{"old-live": true, "old-dead": false, "recent": true} {
		owner, err := d.BuildLogProject(ctx, name)
		if err != nil {
			t.Fatalf("BuildLogProject %s: %v", name, err)
		}
		if got := owner != ""; got != want {
			t.Errorf("%s kept=%v, want %v", name, got, want)
		}
	}
}
