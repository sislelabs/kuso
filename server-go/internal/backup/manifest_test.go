package backup

import "testing"

func TestManifestKey(t *testing.T) {
	got := ManifestKey("acme/acme-db/20260721T120000Z.sql.gz")
	want := "acme/acme-db/20260721T120000Z.sql.gz.manifest.json"
	if got != want {
		t.Fatalf("ManifestKey = %q, want %q", got, want)
	}
}

func TestParseAndArtifactFor(t *testing.T) {
	raw := []byte(`{
	  "schemaVersion":1,"createdAt":"2026-07-21T12:00:00Z",
	  "project":"acme","addon":"acme-db","addonKind":"postgres","producer":"pg_dump",
	  "artifacts":[{"key":"acme/acme-db/x.sql.gz","sha256":"abc","bytes":42,"payloadKind":"pg_dump"}]
	}`)
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if m.AddonKind != "postgres" || len(m.Artifacts) != 1 {
		t.Fatalf("parsed wrong: %#v", m)
	}
	a, ok := m.ArtifactFor("acme/acme-db/x.sql.gz")
	if !ok || a.SHA256 != "abc" || a.Bytes != 42 {
		t.Fatalf("ArtifactFor wrong: %#v %v", a, ok)
	}
	if _, ok := m.ArtifactFor("nope"); ok {
		t.Fatal("ArtifactFor should miss unknown key")
	}
}

func TestParseRejectsFutureSchema(t *testing.T) {
	if _, err := Parse([]byte(`{"schemaVersion":99}`)); err == nil {
		t.Fatal("expected error for future schemaVersion")
	}
}
