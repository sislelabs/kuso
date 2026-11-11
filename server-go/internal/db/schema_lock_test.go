package db

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseAddColumnsIfNotExists(t *testing.T) {
	cases := []struct {
		stmt  string
		table string
		cols  []string
		ok    bool
	}{
		{`ALTER TABLE "User" ADD COLUMN IF NOT EXISTS "instanceRole" TEXT`, "User", []string{"instanceRole"}, true},
		{"ALTER TABLE \"NotificationEvent\"\n    ADD COLUMN IF NOT EXISTS \"classification\" JSONB", "NotificationEvent", []string{"classification"}, true},
		{`ALTER TABLE "UserGroup" ADD COLUMN IF NOT EXISTS "a" TEXT NOT NULL DEFAULT '[,]', ADD COLUMN IF NOT EXISTS b NUMERIC(10,2)`, "UserGroup", []string{"a", "b"}, true},
		{`ALTER TABLE Foo ADD IF NOT EXISTS Bar INT`, "foo", []string{"bar"}, true},
		{`ALTER TABLE "User" ADD COLUMN IF NOT EXISTS "x" TEXT, DROP COLUMN "y"`, "", nil, false},
		{`ALTER TABLE "User" ADD COLUMN "x" TEXT`, "", nil, false},
		{`ALTER TABLE "User" ADD CONSTRAINT "c" UNIQUE ("x")`, "", nil, false},
		{`CREATE TABLE IF NOT EXISTS "X" ("a" INT)`, "", nil, false},
	}
	for _, c := range cases {
		table, cols, ok := parseAddColumnsIfNotExists(c.stmt)
		if ok != c.ok || table != c.table || !reflect.DeepEqual(cols, c.cols) {
			t.Errorf("%q: got (%q, %v, %v), want (%q, %v, %v)", c.stmt, table, cols, ok, c.table, c.cols, c.ok)
		}
	}
}

func shrinkLockRetry(t *testing.T) {
	t.Helper()
	pt, pa, pb, pm := schemaLockTimeout, schemaLockAttempts, schemaLockBackoff, schemaLockMaxBackoff
	schemaLockTimeout, schemaLockAttempts, schemaLockBackoff, schemaLockMaxBackoff = 200*time.Millisecond, 2, 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() {
		schemaLockTimeout, schemaLockAttempts, schemaLockBackoff, schemaLockMaxBackoff = pt, pa, pb, pm
	})
}

// holdReadLock opens a transaction that reads table and keeps its
// ACCESS SHARE lock until the test ends.
func holdReadLock(t *testing.T, d *DB, table string) {
	t.Helper()
	tx, err := d.DB.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	if _, err := tx.Exec(`SELECT * FROM "` + table + `" LIMIT 1`); err != nil {
		t.Fatalf("hold read lock: %v", err)
	}
}

func TestApplySchema_NoOpAddColumnSkipsTableLock(t *testing.T) {
	d := openTestDB(t)
	shrinkLockRetry(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`DROP TABLE IF EXISTS "Db3Probe"; CREATE TABLE "Db3Probe" ("id" INT, "present" TEXT)`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _, _ = d.DB.Exec(`DROP TABLE IF EXISTS "Db3Probe"`) })
	holdReadLock(t, d, "Db3Probe")

	start := time.Now()
	if err := d.applySchemaSQL(ctx, `ALTER TABLE "Db3Probe" ADD COLUMN IF NOT EXISTS "present" TEXT;`); err != nil {
		t.Fatalf("no-op ADD COLUMN behind a reader: %v", err)
	}
	if el := time.Since(start); el > schemaLockTimeout {
		t.Errorf("no-op ADD COLUMN waited %s for the table lock", el)
	}
}

func TestApplySchema_LockTimeoutGivesUp(t *testing.T) {
	d := openTestDB(t)
	shrinkLockRetry(t)
	ctx := context.Background()
	if _, err := d.DB.Exec(`DROP TABLE IF EXISTS "Db3Probe"; CREATE TABLE "Db3Probe" ("id" INT)`); err != nil {
		t.Fatalf("setup: %v", err)
	}
	t.Cleanup(func() { _, _ = d.DB.Exec(`DROP TABLE IF EXISTS "Db3Probe"`) })
	holdReadLock(t, d, "Db3Probe")

	done := make(chan error, 1)
	go func() {
		done <- d.applySchemaSQL(ctx, `ALTER TABLE "Db3Probe" ADD COLUMN IF NOT EXISTS "missing" TEXT;`)
	}()
	select {
	case err := <-done:
		if err == nil || !isLockTimeout(err) || !strings.Contains(err.Error(), "gave up after 2") {
			t.Fatalf("want lock-timeout give-up error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("applySchemaSQL hung behind the reader instead of timing out")
	}

	conn, err := d.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	m := migration{version: 99998, name: "lock_probe", body: `ALTER TABLE "Db3Probe" ADD COLUMN "missing" TEXT;`, checksum: "x"}
	t.Cleanup(func() { _, _ = d.DB.Exec(`DELETE FROM "SchemaMigration" WHERE "version" = 99998`) })
	go func() { done <- d.applyMigration(ctx, conn, m) }()
	select {
	case err := <-done:
		if err == nil || !isLockTimeout(err) {
			t.Fatalf("migration: want lock-timeout error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("migration hung behind the reader instead of timing out")
	}
	go func() { done <- d.applyMigrationXact(ctx, m) }()
	select {
	case err := <-done:
		if err == nil || !isLockTimeout(err) {
			t.Fatalf("pooler-path migration: want lock-timeout error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("pooler-path migration hung behind the reader")
	}
}
