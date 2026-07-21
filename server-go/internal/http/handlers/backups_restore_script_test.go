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
	if _, err := restoreScriptForKind("nats"); err == nil {
		t.Error("nats should be rejected as not restorable")
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
