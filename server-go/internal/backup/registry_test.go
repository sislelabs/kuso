package backup

import (
	"strings"
	"testing"
)

func TestRegistryResolvesKnownKinds(t *testing.T) {
	r := NewDefaultRegistry()
	for kind, wantPayload := range map[string]string{
		"postgres": "pg_dump",
		"redis":    "redis_rdb",
		"mongodb":  "mongodump",
	} {
		p, ok := r.For(kind)
		if !ok {
			t.Fatalf("For(%q) not found", kind)
		}
		if p.PayloadKind() != wantPayload {
			t.Errorf("For(%q).PayloadKind() = %q, want %q", kind, p.PayloadKind(), wantPayload)
		}
	}
}

func TestRegistryUnknownKind(t *testing.T) {
	r := NewDefaultRegistry()
	if _, ok := r.For("nats"); ok {
		t.Error("nats should not be backable yet")
	}
}

func TestPostgresRestoreScriptUnchangedContract(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	for _, want := range []string{"gunzip -c /tmp/dump.sql.gz", "psql", "manifest.json", "sha256sum", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("postgres restore script missing %q", want)
		}
	}
}

func TestMongoRestoreScript(t *testing.T) {
	p, _ := NewDefaultRegistry().For("mongodb")
	s := p.RestoreScript()
	for _, want := range []string{"mongorestore", "--archive", "--gzip", "MONGO_URL", "manifest.json", "MISMATCH"} {
		if !strings.Contains(s, want) {
			t.Errorf("mongo restore script missing %q", want)
		}
	}
}

// TestPostgresRestoreIsAtomicAndFailLoud pins the P1 fix: the restore must
// pipe into psql with ON_ERROR_STOP=1 (no silent partial apply) and
// --single-transaction (all-or-nothing). Without these a broken/incompatible
// dump onto a populated DB would silently no-op or duplicate rows.
func TestPostgresRestoreIsAtomicAndFailLoud(t *testing.T) {
	p, _ := NewDefaultRegistry().For("postgres")
	s := p.RestoreScript()
	for _, want := range []string{"ON_ERROR_STOP=1", "--single-transaction"} {
		if !strings.Contains(s, want) {
			t.Errorf("postgres restore script missing %q — silent-partial guard not in place", want)
		}
	}
	// The apply must still gunzip the artifact and target the addon's DB.
	if !strings.Contains(s, `gunzip -c /tmp/dump.sql.gz`) || !strings.Contains(s, `"${POSTGRES_DB}"`) {
		t.Errorf("postgres restore apply line malformed: %q", s)
	}
}

func TestMysqlProducer(t *testing.T) {
	p, ok := NewDefaultRegistry().For("mysql")
	if !ok {
		t.Fatal("mysql not registered")
	}
	if p.PayloadKind() != "mysqldump" || p.ArtifactExt() != "sql.gz" {
		t.Fatalf("mysql producer metadata wrong: %s/%s", p.PayloadKind(), p.ArtifactExt())
	}
	s := p.RestoreScript()
	for _, want := range []string{"mysql", "MYSQL_HOST", "manifest.json", "MISMATCH", "gunzip"} {
		if !strings.Contains(s, want) {
			t.Errorf("mysql restore script missing %q", want)
		}
	}
}
