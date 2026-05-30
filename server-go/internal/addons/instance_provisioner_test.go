package addons

import "testing"

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
