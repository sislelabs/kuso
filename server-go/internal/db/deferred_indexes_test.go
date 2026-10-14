package db

import (
	"context"
	"testing"
)

// TestDeferredIndexes_NotBuiltInOpen_ThenBuiltAndRecorded is the DB-3
// regression: the LogLine indexes must not be built by runMigrations
// (which runs inside Open, before the listener), and the background
// builder must create them, repair an INVALID leftover, and record the
// stood-in migration. PG-backed.
func TestDeferredIndexes_NotBuiltInOpen_ThenBuiltAndRecorded(t *testing.T) {
	d := openTestDB(t)
	ctx := context.Background()

	for _, ix := range deferredIndexes {
		if _, err := d.ExecContext(ctx, `DROP INDEX IF EXISTS `+quoteIdent(ix.name)); err != nil {
			t.Fatalf("drop %s: %v", ix.name, err)
		}
	}
	if _, err := d.ExecContext(ctx, `DELETE FROM "SchemaMigration" WHERE "version" = 12`); err != nil {
		t.Fatalf("unrecord 0012: %v", err)
	}
	t.Cleanup(func() { _ = d.BuildDeferredIndexes(context.Background(), nil) })

	if err := d.runMigrations(ctx); err != nil {
		t.Fatalf("runMigrations: %v", err)
	}
	indexState := func(name string) (exists, valid bool) {
		t.Helper()
		err := d.QueryRowContext(ctx,
			`SELECT true, indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)`, quoteIdent(name)).Scan(&exists, &valid)
		if err != nil {
			return false, false
		}
		return exists, valid
	}
	recorded := func() bool {
		var n int
		_ = d.QueryRowContext(ctx, `SELECT COUNT(*) FROM "SchemaMigration" WHERE "version" = 12`).Scan(&n)
		return n == 1
	}
	for _, ix := range deferredIndexes {
		if exists, _ := indexState(ix.name); exists {
			t.Errorf("%s was built inside runMigrations (boot path)", ix.name)
		}
	}
	if recorded() {
		t.Error("0012 recorded before its index exists")
	}
	st, _ := d.MigrationState(ctx)
	if st.Pending != 1 {
		t.Errorf("pending = %d, want 1 (0012 deferred)", st.Pending)
	}

	if err := d.BuildDeferredIndexes(ctx, nil); err != nil {
		t.Fatalf("BuildDeferredIndexes: %v", err)
	}
	for _, ix := range deferredIndexes {
		if exists, valid := indexState(ix.name); !exists || !valid {
			t.Errorf("%s exists=%v valid=%v after build", ix.name, exists, valid)
		}
	}
	if !recorded() {
		t.Error("0012 not recorded after its index was built")
	}

	// Simulate a CONCURRENTLY build that was interrupted: the index is
	// left INVALID. Needs superuser to poke pg_index; skip that half if
	// the test role can't.
	const target = "LogLine_project_service_id_idx"
	if _, err := d.ExecContext(ctx,
		`UPDATE pg_index SET indisvalid = false WHERE indexrelid = to_regclass($1)`, quoteIdent(target)); err != nil {
		t.Skipf("cannot mark index invalid (not superuser?): %v", err)
	}
	if _, valid := indexState(target); valid {
		t.Fatal("setup: index still valid")
	}
	if err := d.BuildDeferredIndexes(ctx, nil); err != nil {
		t.Fatalf("BuildDeferredIndexes (repair): %v", err)
	}
	if exists, valid := indexState(target); !exists || !valid {
		t.Errorf("INVALID index not repaired: exists=%v valid=%v", exists, valid)
	}
}
