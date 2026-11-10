package db

import (
	"context"
	"database/sql"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDirectDSN(t *testing.T) {
	t.Setenv("KUSO_DB_DIRECT_DSN", "")
	cases := map[string]string{
		"postgres://u:p@kuso-pgbouncer:6432/kuso?sslmode=disable":          "postgres://u:p@kuso-postgres-rw:5432/kuso?sslmode=disable",
		"postgres://u:p@kuso-pgbouncer.kuso.svc:6432/kuso?sslmode=disable": "postgres://u:p@kuso-postgres-rw.kuso.svc:5432/kuso?sslmode=disable",
		"postgres://u:p@kuso-postgres-rw:5432/kuso?sslmode=disable":        "",
		"postgres://u:p@db.example.com:5432/kuso":                          "",
	}
	for in, want := range cases {
		if got := directDSN(in); got != want {
			t.Errorf("directDSN(%q) = %q, want %q", in, got, want)
		}
	}
	t.Setenv("KUSO_DB_DIRECT_DSN", "postgres://override")
	if got := directDSN("postgres://u:p@db.example.com/kuso"); got != "postgres://override" {
		t.Errorf("env override ignored: %q", got)
	}
}

// pgbouncerDSNs returns a transaction-mode PgBouncer DSN and the direct DSN
// of the Postgres behind it, both as a NON-superuser app role (the role
// defaults must be settable by the app role itself).
func pgbouncerDSNs(t *testing.T) (pooled, direct string) {
	t.Helper()
	pooled, direct = os.Getenv("KUSO_TEST_PGBOUNCER_DSN"), os.Getenv("KUSO_TEST_PGBOUNCER_DIRECT_DSN")
	if pooled == "" || direct == "" {
		t.Skip("KUSO_TEST_PGBOUNCER_DSN / KUSO_TEST_PGBOUNCER_DIRECT_DSN not set")
	}
	return pooled, direct
}

// SC1: statement_timeout can't ride a startup parameter through PgBouncer,
// so it is a role default. New pooled backends must see it, SET LOCAL must
// still raise it inside one transaction, and re-applying changes nothing.
func TestRoleTimeouts_ThroughTransactionPooler(t *testing.T) {
	pooled, _ := pgbouncerDSNs(t)
	ctx := context.Background()
	t.Setenv("KUSO_DB_STATEMENT_TIMEOUT", "1500ms")
	t.Setenv("KUSO_DB_IDLE_TX_TIMEOUT", "")
	raw, err := sql.Open("postgres", pooled)
	if err != nil {
		t.Fatal(err)
	}
	d := &DB{DB: raw}
	t.Cleanup(func() {
		_, _ = raw.Exec(`ALTER ROLE CURRENT_USER IN DATABASE appdb RESET ALL`)
		_ = raw.Close()
	})
	if err := d.applyRoleTimeouts(ctx); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Existing pooled backends keep the old value until PgBouncer recycles
	// them; recycle them now so the next one starts with the new default.
	_, direct := pgbouncerDSNs(t)
	if dc, err := sql.Open("postgres", direct); err == nil {
		_, _ = dc.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = current_user AND pid <> pg_backend_pid()`)
		_ = dc.Close()
	}
	fresh, err := sql.Open("postgres", pooled)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	var st string
	if err := fresh.QueryRow(`SHOW statement_timeout`).Scan(&st); err != nil {
		t.Fatal(err)
	}
	if st != "1500ms" {
		t.Fatalf("statement_timeout on a pooled backend = %q, want 1500ms (backend may predate the change; PgBouncer recycles them)", st)
	}
	if _, err := fresh.Exec(`SELECT pg_sleep(3)`); err == nil {
		t.Fatal("a 3s statement ran past the 1.5s role timeout")
	}
	tx, err := fresh.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SET LOCAL statement_timeout = '10s'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`SELECT pg_sleep(2)`); err != nil {
		t.Fatalf("SET LOCAL did not lift the timeout inside the transaction: %v", err)
	}
	_ = tx.Commit()

	var n int
	if err := raw.QueryRow(`SELECT count(*) FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole WHERE r.rolname = current_user`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("role settings rows = %d (%v), want 1", n, err)
	}
	if err := d.applyRoleTimeouts(ctx); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	t.Setenv("KUSO_DB_STATEMENT_TIMEOUT", "0")
	if err := d.applyRoleTimeouts(ctx); err != nil {
		t.Fatalf("reset: %v", err)
	}
	var cfg sql.NullString
	_ = raw.QueryRow(`SELECT array_to_string(setconfig, ',') FROM pg_db_role_setting s JOIN pg_roles r ON r.oid = s.setrole WHERE r.rolname = current_user`).Scan(&cfg)
	if cfg.String != "idle_in_transaction_session_timeout=300000" {
		t.Fatalf("after reset: %q, want only the idle timeout", cfg.String)
	}
}

// SC2: behind a transaction pooler the migration lock is per transaction.
// Two boots racing the same pending migration must both succeed and
// record it once.
func TestRunMigrationsXact_ThroughTransactionPooler(t *testing.T) {
	pooled, direct := pgbouncerDSNs(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	t.Setenv("KUSO_DB_DIRECT_DSN", direct)
	setup, err := Open(pooled)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_, _ = setup.ExecContext(ctx, `ALTER ROLE CURRENT_USER IN DATABASE appdb RESET ALL`)
	// Not idempotent on purpose: a second apply fails, so only the lock
	// plus the re-check under it lets every racer succeed.
	last := migration{version: 9999, name: "race_probe", body: `CREATE TABLE "lane_e_race_probe" ("id" int)`, checksum: "probe"}
	migs := []migration{last}
	cleanup := func() {
		_, _ = setup.ExecContext(context.Background(), `DROP TABLE IF EXISTS "lane_e_race_probe"`)
		_, _ = setup.ExecContext(context.Background(), `DELETE FROM "SchemaMigration" WHERE "version" = 9999`)
	}
	cleanup()
	defer func() { cleanup(); _ = setup.Close() }()

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			raw, err := sql.Open("postgres", pooled)
			if err != nil {
				errs[i] = err
				return
			}
			defer raw.Close()
			errs[i] = (&DB{DB: raw, pooledOnly: true}).runMigrationsXact(ctx, migs)
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("runner %d: %v", i, err)
		}
	}
	check, _ := sql.Open("postgres", pooled)
	defer check.Close()
	var n int
	if err := check.QueryRowContext(ctx, `SELECT count(*) FROM "SchemaMigration" WHERE "version" = $1`, last.version).Scan(&n); err != nil || n != 1 {
		t.Fatalf("migration %d recorded %d times (%v), want 1", last.version, n, err)
	}
}

// Open through the pooler with the direct path available: maintenance work
// goes direct, with no statement timeout, and migrations take the session lock.
func TestOpen_UsesDirectMaintenancePool(t *testing.T) {
	pooled, direct := pgbouncerDSNs(t)
	t.Setenv("KUSO_DB_DIRECT_DSN", direct)
	d, err := Open(pooled)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer d.Close()
	t.Cleanup(func() { _, _ = d.Exec(`ALTER ROLE CURRENT_USER IN DATABASE appdb RESET ALL`) })
	if d.maint == nil || d.pooledOnly {
		t.Fatalf("maint=%v pooledOnly=%v, want a direct maintenance pool", d.maint != nil, d.pooledOnly)
	}
	var st string
	if err := d.Maintenance().QueryRow(`SHOW statement_timeout`).Scan(&st); err != nil || st != "0" {
		t.Fatalf("maintenance statement_timeout = %q (%v), want 0", st, err)
	}
}
