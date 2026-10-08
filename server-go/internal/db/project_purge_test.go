package db

import (
	"context"
	"testing"
)

// A project deleted and re-created under the same name must not inherit
// the old one's alert rules, logs, error events or uptime state.
func TestPurgeProjectState(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()
	for _, p := range []string{"purge-doomed", "purge-keep"} {
		for _, q := range []string{
			`INSERT INTO "AlertRule" ("id","name","kind","project") VALUES ($1 || '-rule', 'r', 'log_match', $1)`,
			`INSERT INTO "LogLine" ("ts","pod","project","line") VALUES (now(), 'pod', $1, 'l')`,
			`INSERT INTO "ErrorEvent" ("project","service","fingerprint","message","ts") VALUES ($1, 's', 'f', 'm', now())`,
			`INSERT INTO "UptimeState" ("namespace","env","project","service") VALUES ('kuso', $1 || '-env', $1, 's')`,
		} {
			if _, err := d.ExecContext(ctx, q, p); err != nil {
				t.Fatalf("seed %s: %v", p, err)
			}
		}
	}
	t.Cleanup(func() { _ = d.PurgeProjectState(ctx, "purge-keep") })

	if err := d.PurgeProjectState(ctx, "purge-doomed"); err != nil {
		t.Fatalf("PurgeProjectState: %v", err)
	}
	for _, table := range []string{"AlertRule", "LogLine", "ErrorEvent", "UptimeState"} {
		var doomed, kept int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE "project"='purge-doomed'), count(*) FILTER (WHERE "project"='purge-keep') FROM "`+table+`"`).Scan(&doomed, &kept); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if doomed != 0 || kept != 1 {
			t.Errorf("%s: doomed=%d kept=%d, want 0/1", table, doomed, kept)
		}
	}
}
