package db

import (
	"context"
	"testing"
)

// TestParseMigrationName covers the NNNN_label.sql parsing + its
// rejections (the guards that stop a typo from reordering/shadowing).
func TestParseMigrationName(t *testing.T) {
	t.Parallel()
	ok := []struct {
		in    string
		ver   int
		label string
	}{
		{"0001_audit_project_index.sql", 1, "audit_project_index"},
		{"0042_add_widget.sql", 42, "add_widget"},
		{"0010_multi_word_label.sql", 10, "multi_word_label"},
	}
	for _, tc := range ok {
		v, l, err := parseMigrationName(tc.in)
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.in, err)
			continue
		}
		if v != tc.ver || l != tc.label {
			t.Errorf("%s → (%d,%q), want (%d,%q)", tc.in, v, l, tc.ver, tc.label)
		}
	}

	bad := []string{
		"noversion.sql",     // no underscore
		"abc_label.sql",     // non-integer version
		"0000_baseline.sql", // 0 is reserved for the schema.sql baseline
		"_label.sql",        // empty version
		"-5_back.sql",       // negative
	}
	for _, in := range bad {
		if _, _, err := parseMigrationName(in); err == nil {
			t.Errorf("%s: expected a parse error, got nil", in)
		}
	}
}

// TestLoadMigrations checks the embedded migrations load, parse, sort,
// and have non-empty checksums — and that the first real migration is
// present (guards against an accidental embed glob break).
func TestLoadMigrations(t *testing.T) {
	t.Parallel()
	migs, err := loadMigrations()
	if err != nil {
		t.Fatalf("loadMigrations: %v", err)
	}
	if len(migs) == 0 {
		t.Fatal("no migrations loaded — embed glob may be broken")
	}
	// Ascending by version + unique.
	seen := map[int]bool{}
	for i, m := range migs {
		if i > 0 && m.version <= migs[i-1].version {
			t.Errorf("migrations not strictly ascending at %d: %d after %d", i, m.version, migs[i-1].version)
		}
		if seen[m.version] {
			t.Errorf("duplicate version %d", m.version)
		}
		seen[m.version] = true
		if m.checksum == "" || m.body == "" {
			t.Errorf("migration %04d has empty checksum/body", m.version)
		}
	}
	// 0001 is the audit index migration we shipped.
	if migs[0].version != 1 {
		t.Errorf("first migration version = %d, want 1", migs[0].version)
	}
}

// TestRunMigrations_AppliesAndRecords is PG-backed: applies the baseline
// + migrations, confirms SchemaMigration is populated, and that a second
// run is a clean no-op (idempotent).
func TestRunMigrations_AppliesAndRecords(t *testing.T) {
	d := openTestDB(t) // skips without KUSO_TEST_PG_DSN
	ctx := context.Background()

	// openTestDB already ran Open → runMigrations. Verify state.
	st, err := d.MigrationState(ctx)
	if err != nil {
		t.Fatalf("MigrationState: %v", err)
	}
	if !st.BaselineApplied {
		t.Error("baseline should be applied")
	}
	migs, _ := loadMigrations()
	if st.Applied != len(migs) {
		t.Errorf("applied = %d, want %d (all migrations)", st.Applied, len(migs))
	}
	if st.Pending != 0 {
		t.Errorf("pending = %d, want 0", st.Pending)
	}

	// SchemaMigration has a row per migration.
	var n int
	if err := d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "SchemaMigration"`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != len(migs) {
		t.Errorf("SchemaMigration rows = %d, want %d", n, len(migs))
	}

	// Re-running is a clean no-op (idempotent): no error, same count.
	if err := d.runMigrations(ctx); err != nil {
		t.Fatalf("second runMigrations: %v", err)
	}
	var n2 int
	_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "SchemaMigration"`).Scan(&n2)
	if n2 != n {
		t.Errorf("re-run changed migration count: %d → %d", n, n2)
	}

	// The 0001 index actually exists.
	var idx int
	if err := d.QueryRowContext(ctx,
		`SELECT 1 FROM pg_indexes WHERE indexname = 'Audit_pipeline_id_idx'`).Scan(&idx); err != nil {
		t.Errorf("Audit_pipeline_id_idx not created: %v", err)
	}
}
