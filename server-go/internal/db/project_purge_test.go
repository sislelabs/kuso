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
			`INSERT INTO "EnvHint" ("project","service","name","lastSeen") VALUES ($1, 's', 'FOO', now())`,
			`INSERT INTO "VulnPolicy" ("project","service","mode") VALUES ($1, 's', 'off')`,
			`INSERT INTO "VulnIgnore" ("scope","project","service","vulnId","reason") VALUES ('service', $1, 's', 'CVE-1', 'r')`,
			`INSERT INTO "VulnImage" ("digest","imageRef","ownerKind","project","service") VALUES ('sha256:x', 'r', 'service', $1, 's')`,
			`INSERT INTO "VulnNotified" ("ownerKey","vulnId") VALUES ('service:' || $1 || '/s', 'CVE-1')`,
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
	for _, table := range []string{"AlertRule", "LogLine", "ErrorEvent", "UptimeState", "EnvHint", "VulnPolicy", "VulnIgnore", "VulnImage"} {
		var doomed, kept int
		if err := d.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE "project"='purge-doomed'), count(*) FILTER (WHERE "project"='purge-keep') FROM "`+table+`"`).Scan(&doomed, &kept); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if doomed != 0 || kept != 1 {
			t.Errorf("%s: doomed=%d kept=%d, want 0/1", table, doomed, kept)
		}
	}
	var left []string
	rows, err := d.QueryContext(ctx, `SELECT "ownerKey" FROM "VulnNotified"`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		left = append(left, k)
	}
	if len(left) != 1 || left[0] != "service:purge-keep/s" {
		t.Errorf("VulnNotified left = %v, want only purge-keep's", left)
	}
}
