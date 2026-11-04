package handlers

import (
	"strings"
	"testing"
)

func TestRestoreScriptForKind(t *testing.T) {
	s, err := restoreScriptForKind("postgres")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "psql") || !strings.Contains(s, "MISMATCH") {
		t.Errorf("postgres restore script wrong: %q", s)
	}
	m, err := restoreScriptForKind("mongodb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m, "mongorestore") {
		t.Errorf("mongodb restore script wrong: %q", m)
	}
	my, err := restoreScriptForKind("mysql")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(my, "mysql") {
		t.Errorf("mysql restore script wrong")
	}
	if _, err := restoreScriptForKind("nats"); err == nil {
		t.Error("nats should be rejected as not restorable")
	}
	// DATA-12: redis backups exist but restore is manual; the API must not
	// mint a Job that always exits 1.
	if _, err := restoreScriptForKind("redis"); err == nil {
		t.Error("redis has no automated restore and must be rejected")
	}
}

// TestRestoreNeedsConfirm: every restore (in place, aliased, or into a
// sibling) must echo the destination name. srcName/destName are kept in the
// table to show which CR each case resolves to; the gate no longer exempts
// distinct destinations (WEB-03).
func TestRestoreNeedsConfirm(t *testing.T) {
	cases := []struct {
		name                         string
		srcName, destName, destAddon string
		confirm                      string
		want                         bool
	}{
		{"in-place no confirm", "myproj-pg", "myproj-pg", "pg", "", true},
		{"in-place wrong confirm", "myproj-pg", "myproj-pg", "pg", "nope", true},
		{"in-place correct confirm", "myproj-pg", "myproj-pg", "pg", "pg", false},
		// The aliasing bypass: URL addon "pg", into "myproj-pg" — both resolve
		// to the same CR. Raw-string check (destAddon != addon) would have
		// exempted this; resolved-name check catches it.
		{"alias bypass no confirm", "myproj-pg", "myproj-pg", "myproj-pg", "", true},
		{"alias bypass correct confirm", "myproj-pg", "myproj-pg", "myproj-pg", "myproj-pg", false},
		// WEB-03: dumps are --clean, so a sibling target is overwritten too and
		// needs the same typed confirmation.
		{"distinct sibling no confirm", "myproj-pg", "myproj-pg2", "pg2", "", true},
		{"distinct sibling confirms source instead", "myproj-pg", "myproj-pg2", "pg2", "pg", true},
		{"distinct sibling correct confirm", "myproj-pg", "myproj-pg2", "pg2", "pg2", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := restoreNeedsConfirm(tc.destAddon, tc.confirm)
			if got != tc.want {
				t.Errorf("restoreNeedsConfirm(%q,%q) = %v, want %v",
					tc.destAddon, tc.confirm, got, tc.want)
			}
		})
	}
}

func TestIsManifestKey(t *testing.T) {
	if !isManifestKey("acme/acme-db/x.sql.gz.manifest.json") {
		t.Error("manifest key should be detected")
	}
	if isManifestKey("acme/acme-db/x.sql.gz") {
		t.Error("artifact key is not a manifest")
	}
}
