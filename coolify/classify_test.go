package coolify

import "testing"

// Every engine the kusoaddon chart implements must migrate, and must map
// to an addon kind — a "migrate" verdict with no kind is silently skipped
// on apply.
func TestClassifyDatabase_ChartBackedEnginesMigrate(t *testing.T) {
	cases := []struct {
		coolifyType, kind, action string
	}{
		{"standalone-postgresql", "postgres", "migrate"},
		{"standalone-redis", "redis", "migrate"},
		{"standalone-mysql", "mysql", "migrate"},
		{"standalone-mongodb", "mongodb", "migrate"},
		{"standalone-clickhouse", "clickhouse", "migrate"},
		// mariadb lands on the mysql addon — a different engine, so it
		// stays a flag for a human to check dump compatibility.
		{"standalone-mariadb", "mysql", "flag"},
		{"standalone-dragonfly", "", "flag"},
		{"standalone-keydb", "", "flag"},
	}
	for _, c := range cases {
		v := ClassifyDatabase(Database{DatabaseType: c.coolifyType})
		if v.Action != c.action {
			t.Errorf("%s: action=%q (%s), want %q", c.coolifyType, v.Action, v.Reason, c.action)
		}
		if got := AddonKindFromCoolify(c.coolifyType); got != c.kind {
			t.Errorf("%s: kind=%q, want %q", c.coolifyType, got, c.kind)
		}
	}
}
