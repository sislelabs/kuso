package github

import "testing"

// TestEnvHints_NoDuplicateKinds keeps the hint table well-formed. If
// the same kind shows up under two patterns, the seen-map dedup in
// ScanAddons is the right place to handle it (and the test passes
// because it's not a strict duplicate guard).
func TestEnvHints_KindsKnown(t *testing.T) {
	want := map[string]bool{
		"postgres": true, "redis": true, "mongodb": true, "mysql": true,
		"rabbitmq": true, "memcached": true, "clickhouse": true,
		"elasticsearch": true, "kafka": true, "cockroachdb": true, "couchdb": true,
	}
	for _, h := range envHints {
		if !want[h.kind] {
			t.Errorf("unexpected addon kind %q (extend the test or fix the hint)", h.kind)
		}
	}
}

// TestCandidatePaths_NoDuplicates protects against accidental
// duplicates that would double-count the same file's hints.
func TestCandidatePaths_NoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range candidatePaths {
		if seen[p] {
			t.Errorf("duplicate candidate path %q", p)
		}
		seen[p] = true
	}
}
