package handlers

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func TestBlockedSQLTxControl(t *testing.T) {
	t.Parallel()
	for _, q := range []string{
		"COMMIT", "commit;", "  END", "ROLLBACK", "abort", "BEGIN", "start transaction",
		"SAVEPOINT a", "release a", "PREPARE TRANSACTION 'x'", "SET TRANSACTION READ WRITE",
		"set session characteristics as transaction read write", "RESET ALL", "DISCARD ALL",
		"/* hi */ COMMIT", "-- x\nCOMMIT",
	} {
		if blockedSQLTxControl(q) == "" {
			t.Errorf("blockedSQLTxControl(%q) allowed, want blocked", q)
		}
	}
	for _, q := range []string{
		"SELECT 1", "select case when x then 1 end from t", "SELECT * FROM commits",
		"WITH ended AS (SELECT 1) SELECT * FROM ended", "SELECT 'COMMIT'",
	} {
		if r := blockedSQLTxControl(q); r != "" {
			t.Errorf("blockedSQLTxControl(%q) = %q, want allowed", q, r)
		}
	}
}

// DATA-3: over the simple query protocol `COMMIT; DROP TABLE t` ended the
// read-only transaction and dropped the table. Needs a real Postgres.
func TestRunReadOnlyPGQuery_RejectsMultiStatement(t *testing.T) {
	dsn := os.Getenv("KUSO_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("KUSO_TEST_PG_DSN not set")
	}
	ctx := context.Background()
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`DROP TABLE IF EXISTS zz_ro_victim; CREATE TABLE zz_ro_victim (id int); INSERT INTO zz_ro_victim VALUES (1)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(`DROP TABLE IF EXISTS zz_ro_victim`) })

	for _, q := range []string{
		"COMMIT; DROP TABLE zz_ro_victim; SELECT 1",
		"SELECT 1; DROP TABLE zz_ro_victim",
		"DROP TABLE zz_ro_victim",
		"DELETE FROM zz_ro_victim",
	} {
		if _, _, err := runReadOnlyPGQuery(ctx, conn, q, 10); err == nil {
			t.Errorf("%q: accepted, want error", q)
		}
	}
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM zz_ro_victim`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("victim table after attacks: n=%d err=%v", n, err)
	}

	out, _, err := runReadOnlyPGQuery(ctx, conn, "SELECT id, now()::timestamp(6) AS ts, NULL::text AS z FROM zz_ro_victim", 10)
	if err != nil {
		t.Fatalf("plain SELECT: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0][0] != "1" || !out.Nulls[0][2] {
		t.Fatalf("plain SELECT result = %+v", out)
	}
}
