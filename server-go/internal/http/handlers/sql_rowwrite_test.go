package handlers

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"
)

// DATA-10: the grid sends displayed cells back as the primary key. With
// second-truncated timestamps a (tenant, created_at) key matched nothing, or
// a different row that happened to sit on the whole second.
func TestRowEditor_TimestampKeyRoundTrip(t *testing.T) {
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
	if _, err := conn.Exec(`DROP TABLE IF EXISTS zz_ts_pk;
		CREATE TABLE zz_ts_pk (tenant int, created_at timestamptz, plain timestamp, note text, PRIMARY KEY (tenant, created_at));
		INSERT INTO zz_ts_pk VALUES
			(1, '2024-01-02 03:04:05+00', '2024-01-02 03:04:05', 'whole'),
			(1, '2024-01-02 03:04:05.123456+00', '2024-01-02 03:04:05.123456', 'micro')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(`DROP TABLE IF EXISTS zz_ts_pk`) })

	out, _, err := runReadOnlyPGQuery(ctx, conn, `SELECT tenant, created_at, plain FROM zz_ts_pk WHERE note = 'micro'`, 10)
	if err != nil || len(out.Rows) != 1 {
		t.Fatalf("read: %v %+v", err, out)
	}
	key := map[string]cellValue{
		"tenant":     {Value: out.Rows[0][0]},
		"created_at": {Value: out.Rows[0][1]},
	}

	q, args, err := buildUpdate("public", "zz_ts_pk", map[string]cellValue{"note": {Value: "edited"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := updateExactlyOne(ctx, conn, q, args); err != nil {
		t.Fatalf("update by displayed key %v: %v", key, err)
	}
	var whole string
	if err := conn.QueryRow(`SELECT note FROM zz_ts_pk WHERE created_at = '2024-01-02 03:04:05+00'`).Scan(&whole); err != nil || whole != "whole" {
		t.Fatalf("whole-second row changed: %q %v", whole, err)
	}

	// The timestamp-without-tz column must round-trip too.
	var hit int
	if err := conn.QueryRow(`SELECT count(*) FROM zz_ts_pk WHERE plain = $1`, out.Rows[0][2]).Scan(&hit); err != nil || hit != 1 {
		t.Fatalf("plain timestamp %q matched %d rows (%v)", out.Rows[0][2], hit, err)
	}

	q, args, err = buildDelete("public", "zz_ts_pk", key)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := deleteExactlyOne(ctx, conn, q, args); err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
	if _, err := deleteExactlyOne(ctx, conn, q, args); !errors.Is(err, errNoRowMatched) {
		t.Fatalf("second delete: %v, want errNoRowMatched", err)
	}
}

// A multi-row match (inheritance, partitioned parent) must be rolled back,
// not reported as a 409 after the rows are already gone.
func TestRowEditor_MultiRowWriteRolledBack(t *testing.T) {
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
	if _, err := conn.Exec(`DROP TABLE IF EXISTS zz_multi;
		CREATE TABLE zz_multi (id int, note text);
		INSERT INTO zz_multi VALUES (1, 'a'), (1, 'b')`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Exec(`DROP TABLE IF EXISTS zz_multi`) })
	key := map[string]cellValue{"id": {Value: "1"}}

	q, args, _ := buildUpdate("public", "zz_multi", map[string]cellValue{"note": {Value: "x"}}, key)
	if _, _, err := updateExactlyOne(ctx, conn, q, args); !errors.Is(err, errMultiRowWrite) {
		t.Fatalf("update: %v, want errMultiRowWrite", err)
	}
	q, args, _ = buildDelete("public", "zz_multi", key)
	if _, err := deleteExactlyOne(ctx, conn, q, args); !errors.Is(err, errMultiRowWrite) {
		t.Fatalf("delete: %v, want errMultiRowWrite", err)
	}
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM zz_multi WHERE note IN ('a','b')`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows after refused writes: %d (%v), want 2 untouched", n, err)
	}
}
