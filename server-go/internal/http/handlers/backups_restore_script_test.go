package handlers

import (
	"strings"
	"testing"
)

func TestRestoreScriptVerifiesChecksum(t *testing.T) {
	s := restoreScript()
	for _, want := range []string{
		"manifest.json",
		"sha256sum",
		"MISMATCH",
		"no manifest", // backward-compat warning branch
	} {
		if !strings.Contains(s, want) {
			t.Errorf("restore script missing %q", want)
		}
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
