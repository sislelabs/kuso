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
