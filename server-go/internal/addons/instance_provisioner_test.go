package addons

import (
	"strings"
	"testing"
)

// TestPgIdentifier_CloneDistinctFromSource is the load-bearing
// correctness guarantee for instance-pg PREVIEW cloning: a per-PR clone
// addon (name "<addon>-pr-N") must map to a DIFFERENT database than its
// source ("<addon>"), or a preview would read/write the production DB.
func TestPgIdentifier_CloneDistinctFromSource(t *testing.T) {
	t.Parallel()
	source := pgIdentifier("tickero", "db")
	clone := pgIdentifier("tickero", "db-pr-35")
	if source == clone {
		t.Fatalf("clone DB identifier (%q) must differ from source (%q)", clone, source)
	}
	if source != "tickero_db" {
		t.Errorf("source identifier = %q, want tickero_db", source)
	}
	if clone != "tickero_db_pr_35" {
		t.Errorf("clone identifier = %q, want tickero_db_pr_35", clone)
	}
}

// TestPgIdentifier_Bounded: long names hash down to ≤63 chars (the
// Postgres identifier limit) and stay distinct.
func TestPgIdentifier_Bounded(t *testing.T) {
	t.Parallel()
	long := pgIdentifier("a-very-long-project-name-that-keeps-going", "an-addon-name-also-quite-long-pr-1234")
	if len(long) > 63 {
		t.Errorf("identifier %q is %d chars, exceeds Postgres 63 limit", long, len(long))
	}
	// Two different long inputs must not collide post-hash.
	other := pgIdentifier("a-very-long-project-name-that-keeps-going", "an-addon-name-also-quite-long-pr-1235")
	if long == other {
		t.Errorf("distinct long inputs collided: %q", long)
	}
}

// TestPoolerDSN routes a direct per-project DSN through the cluster-DB
// PgBouncer: host gets the "-pooler" suffix, port becomes 6432, and
// sslmode flips to disable (the pooler serves plaintext on :6432). Used
// to write the project's DATABASE_URL through the pooler by default.
func TestPoolerDSN(t *testing.T) {
	t.Parallel()
	in := "postgres://jiramudira_pg:secret@kuso-instance-pg:5432/jiramudira_pg?sslmode=require"
	got, err := poolerDSN(in)
	if err != nil {
		t.Fatalf("poolerDSN: %v", err)
	}
	want := "postgres://jiramudira_pg:secret@kuso-instance-pg-pooler:6432/jiramudira_pg?sslmode=disable"
	if got != want {
		t.Errorf("poolerDSN:\n got %s\nwant %s", got, want)
	}
}

// TestPoolerDSN_PreservesCredsAndDB makes sure the user/password/db aren't
// mangled when reserved chars are present.
func TestPoolerDSN_PreservesCredsAndDB(t *testing.T) {
	t.Parallel()
	in := "postgres://u%40x:p%2Fw@kuso-instance-pg:5432/my_db?sslmode=require"
	got, err := poolerDSN(in)
	if err != nil {
		t.Fatalf("poolerDSN: %v", err)
	}
	if !strings.Contains(got, "u%40x:p%2Fw@kuso-instance-pg-pooler:6432/my_db") {
		t.Errorf("creds/db not preserved: %s", got)
	}
}

// TestInstanceAddonConnData_DirectURLNeverPooled is the regression guard for
// the Prisma-on-PgBouncer CrashLoopBackOff: when the shared instance has a
// pooler, DATABASE_URL gets rewritten to the -pooler:6432 transaction-pooling
// endpoint, but DIRECT_URL MUST stay the un-pooled :5432 input DSN so Prisma's
// `directUrl` runs migrations (session-scoped pg_advisory_lock 72707369) on a
// sticky session instead of leaking the lock across the pooler txn boundary.
func TestInstanceAddonConnData_DirectURLNeverPooled(t *testing.T) {
	const dsn = "postgres://berivangold_db:secret@kuso-instance-pg:5432/berivangold_db?sslmode=disable"

	data, err := instanceAddonConnData(dsn, "secret", true /* poolerExists */)
	if err != nil {
		t.Fatalf("instanceAddonConnData: %v", err)
	}

	if got := string(data["DIRECT_URL"]); got != dsn {
		t.Errorf("DIRECT_URL = %q, want the un-pooled input DSN %q", got, dsn)
	}
	if got := string(data["DIRECT_URL"]); strings.Contains(got, "-pooler") || strings.Contains(got, ":6432") {
		t.Errorf("DIRECT_URL is routed through the pooler (%q) — migrations would hit the advisory-lock leak", got)
	}
	if got := string(data["DATABASE_URL"]); !strings.Contains(got, "-pooler:6432") {
		t.Errorf("DATABASE_URL = %q, want the pooler endpoint when poolerExists", got)
	}
	if string(data["DATABASE_URL"]) == string(data["DIRECT_URL"]) {
		t.Error("DATABASE_URL and DIRECT_URL are identical with a pooler present; the split is the whole point")
	}
}

// TestInstanceAddonConnData_NoPooler: with no pooler, DATABASE_URL and
// DIRECT_URL collapse to the same direct DSN and POOLER_* keys stay empty
// (emitted-but-blank, not absent — apps read "" as "pooler not enabled").
func TestInstanceAddonConnData_NoPooler(t *testing.T) {
	const dsn = "postgres://app:secret@some-external-pg:5432/app?sslmode=require"

	data, err := instanceAddonConnData(dsn, "secret", false /* poolerExists */)
	if err != nil {
		t.Fatalf("instanceAddonConnData: %v", err)
	}
	if got := string(data["DIRECT_URL"]); got != dsn {
		t.Errorf("DIRECT_URL = %q, want %q", got, dsn)
	}
	if got := string(data["DATABASE_URL"]); got != dsn {
		t.Errorf("DATABASE_URL = %q, want the direct DSN %q when no pooler", got, dsn)
	}
	for _, k := range []string{"POOLER_HOST", "POOLER_PORT", "POOLER_URL"} {
		if got := string(data[k]); got != "" {
			t.Errorf("%s = %q, want empty when no pooler", k, got)
		}
	}
}
