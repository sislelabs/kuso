package db

import "testing"

func TestSchemaAheadMessage(t *testing.T) {
	t.Parallel()
	if got := schemaAheadMessage(12, 12); got != "" {
		t.Errorf("equal versions: %q, want empty", got)
	}
	if got := schemaAheadMessage(3, 12); got != "" {
		t.Errorf("binary ahead of db (pending migrations): %q, want empty", got)
	}
	if got := schemaAheadMessage(14, 12); got == "" {
		t.Error("db ahead of binary must be reported")
	}
}
