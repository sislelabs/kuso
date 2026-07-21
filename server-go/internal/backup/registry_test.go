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
