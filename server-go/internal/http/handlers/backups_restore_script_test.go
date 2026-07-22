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
}

// TestInPlaceRestoreNeedsConfirm pins the P1 fix: the confirm gate compares
// RESOLVED CR names, so short-name/FQN aliasing can't bypass it. Both `pg` and
// `myproj-pg` resolve to CR "myproj-pg"; a caller doing addon=pg into=myproj-pg
// is still overwriting the source in place and MUST confirm.
func TestInPlaceRestoreNeedsConfirm(t *testing.T) {
	cases := []struct {
		name                          string
		srcName, destName, destAddon  string
		confirm                       string
		want                          bool
	}{
		{"in-place no confirm", "myproj-pg", "myproj-pg", "pg", "", true},
		{"in-place wrong confirm", "myproj-pg", "myproj-pg", "pg", "nope", true},
		{"in-place correct confirm", "myproj-pg", "myproj-pg", "pg", "pg", false},
		// The aliasing bypass: URL addon "pg", into "myproj-pg" — both resolve
		// to the same CR. Raw-string check (destAddon != addon) would have
		// exempted this; resolved-name check catches it.
		{"alias bypass no confirm", "myproj-pg", "myproj-pg", "myproj-pg", "", true},
		{"alias bypass correct confirm", "myproj-pg", "myproj-pg", "myproj-pg", "myproj-pg", false},
		// Genuinely distinct sibling destination — exempt regardless of confirm.
		{"distinct sibling exempt", "myproj-pg", "myproj-pg2", "pg2", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inPlaceRestoreNeedsConfirm(tc.srcName, tc.destName, tc.destAddon, tc.confirm)
			if got != tc.want {
				t.Errorf("inPlaceRestoreNeedsConfirm(%q,%q,%q,%q) = %v, want %v",
					tc.srcName, tc.destName, tc.destAddon, tc.confirm, got, tc.want)
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
