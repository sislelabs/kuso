package handlers

import (
	"strings"
	"testing"
)

// OPS-4: SQL console audit rows are readable by project viewers, so the
// inserted values and query literals must not reach the message.
func TestSQLAuditMessages_OmitValues(t *testing.T) {
	t.Parallel()
	msg := sqlWriteAuditMessage("insert", "public", "users", map[string]cellValue{
		"email": {Value: "alice@example.com"}, "password_hash": {Value: "$2b$10$secrethash"},
	})
	for _, v := range []string{"alice@example.com", "secrethash"} {
		if strings.Contains(msg, v) {
			t.Errorf("write audit leaked %q: %s", v, msg)
		}
	}
	if !strings.Contains(msg, "insert public.users columns=[email,password_hash]") {
		t.Errorf("write audit lost op/table/columns: %s", msg)
	}

	cases := map[string]string{
		`SELECT * FROM users WHERE email = 'bob@x.io' AND id = 42`:     `SELECT * FROM users WHERE email = ? AND id = ?`,
		`INSERT INTO t2 ("a b", c) VALUES ('it''s', E'x\'tok', 1.5e3)`: `INSERT INTO t2 ("a b", c) VALUES (?, ?, ?)`,
		`SELECT $$tok-a$$, $q$tok-b$q$, $1 FROM t -- tok-c` + "\n;":    `SELECT ?, ?, $1 FROM t -- ?` + "\n;",
		`SELECT /* tok-d */ 1`:       `SELECT /* ? */ ?`,
		`SELECT 'unterminated tok-e`: `SELECT ?`,
	}
	for in, want := range cases {
		got := sqlQueryAuditMessage(in, false)
		if !strings.HasSuffix(got, "] "+want) {
			t.Errorf("query %q\n got %q\nwant suffix %q", in, got, want)
		}
		for _, tok := range []string{"bob@x.io", "it''s", "tok"} {
			if strings.Contains(got, tok) {
				t.Errorf("query audit leaked %q: %s", tok, got)
			}
		}
	}
}

// HTTP-1: ClickHouse treats \' inside a plain '…' literal as an escaped
// quote, so the Postgres scanning rules closed the literal early and wrote
// the rest of it into the viewer-readable audit row.
func TestSQLAuditMessages_ClickHouseBackslashEscape(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`SELECT * FROM events WHERE token = 'a\'sk_live_SECRET123'`: `SELECT * FROM events WHERE token = ?`,
		`SELECT * FROM t WHERE pw = 'p\'' OR pw = 'hunter2'`:        `SELECT * FROM t WHERE pw = ? OR pw = ?`,
		`SELECT 'x\\' , 'hunter2'`:                                  `SELECT ? , ?`,
	}
	for in, want := range cases {
		got := sqlQueryAuditMessage(in, true)
		if !strings.HasSuffix(got, "] "+want) {
			t.Errorf("query %q\n got %q\nwant suffix %q", in, got, want)
		}
		for _, tok := range []string{"SECRET123", "hunter2"} {
			if strings.Contains(got, tok) {
				t.Errorf("query audit leaked %q: %s", tok, got)
			}
		}
	}
	// Postgres (standard_conforming_strings=on) keeps a backslash literal.
	if got := sqlQueryAuditMessage(`SELECT 'a\' , 'hunter2'`, false); !strings.HasSuffix(got, "] SELECT ? , ?") {
		t.Errorf("postgres literal ending in a backslash: %q", got)
	}
}
