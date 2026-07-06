package handlers

import "testing"

func TestBlockedClickHouseBuiltin(t *testing.T) {
	blocked := []string{
		"SELECT * FROM file('/etc/passwd', 'CSV')",
		"select url('http://169.254.169.254/', 'CSV')",
		"SELECT * FROM remote('other:9000', system.tables)",
		"SELECT * FROM remoteSecure('h', db.t)",
		"SELECT mysql('h:3306','db','t','u','p')",
		"SELECT * FROM postgresql('h','db','t','u','p')",
		"SELECT * FROM s3('https://b/k','CSV')",
		"SELECT * FROM hdfs('hdfs://x')",
		"SELECT 1 INTO OUTFILE '/tmp/x'",
		"SELECT * FROM infile('x')",
		// Evasion: ClickHouse accepts whitespace before the '(', so a naive
		// "file(" substring check misses these. The normalizer must catch them.
		"SELECT * FROM file ('/etc/passwd','CSV')",
		"SELECT * FROM url\t('http://x/','CSV')",
		"SELECT * FROM remote  ('h', t)",
		"SELECT * FROM url\n('http://x/','CSV')",
		// Evasion: block comment between name and paren gets stripped first.
		"SELECT * FROM file/**/('/x','CSV')",
		// Newer network table functions.
		"SELECT * FROM mongodb('h','db','c','u','p','{}')",
		"SELECT * FROM iceberg('s3://b/k')",
	}
	for _, q := range blocked {
		if reason := blockedClickHouseBuiltin(q); reason == "" {
			t.Errorf("expected %q to be blocked, but it passed", q)
		}
	}

	allowed := []string{
		"SELECT count() FROM events",
		"SELECT * FROM litetrack.events WHERE project_id = 'p' LIMIT 10",
		"SELECT is_bot, count() FROM events GROUP BY is_bot",
		"SELECT profile_name FROM system.settings", // 'file' not present as a call
		// A column/table whose NAME contains a blocked word but isn't a call.
		"SELECT filename FROM logs",
		"SELECT * FROM my_files WHERE url_path = '/x'",
		// A comment mentioning a blocked function is stripped, so it's allowed.
		"SELECT count() FROM events -- reads the file table? no",
	}
	for _, q := range allowed {
		if reason := blockedClickHouseBuiltin(q); reason != "" {
			t.Errorf("expected %q to pass, but it was blocked: %s", q, reason)
		}
	}
}

func TestParseClickHouseJSONCompact(t *testing.T) {
	body := []byte(`{
		"meta": [{"name":"is_bot"},{"name":"n"},{"name":"note"}],
		"data": [[0, 20, "ok"], [1, 3, null], [0, 5, "x"]],
		"rows": 3
	}`)

	// limit 2 → 2 rows returned, Truncated true (we got 3).
	out, err := parseClickHouseJSONCompact(body, 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := out.Columns; len(got) != 3 || got[0] != "is_bot" || got[2] != "note" {
		t.Fatalf("columns = %v, want [is_bot n note]", got)
	}
	if !out.Truncated {
		t.Errorf("expected Truncated=true (3 rows, limit 2)")
	}
	if len(out.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(out.Rows))
	}
	// String cell unquoted; number as compact JSON; null → empty.
	if out.Rows[0][0] != "0" || out.Rows[0][1] != "20" || out.Rows[0][2] != "ok" {
		t.Errorf("row0 = %v, want [0 20 ok]", out.Rows[0])
	}
	if out.Rows[1][2] != "" {
		t.Errorf("null cell = %q, want empty string", out.Rows[1][2])
	}

	// limit large enough → no truncation.
	out2, _ := parseClickHouseJSONCompact(body, 10)
	if out2.Truncated {
		t.Errorf("expected no truncation with limit 10")
	}
	if len(out2.Rows) != 3 {
		t.Errorf("rows = %d, want 3", len(out2.Rows))
	}
}

func TestStringifyJSONCell(t *testing.T) {
	cases := map[string]string{
		`"hello"`:      "hello",
		`42`:           "42",
		`3.14`:         "3.14",
		`null`:         "",
		`true`:         "true",
		`["a","b"]`:    `["a","b"]`,
		`{"k":1}`:      `{"k":1}`,
		`"with\"quote"`: `with"quote`,
	}
	for in, want := range cases {
		if got := stringifyJSONCell([]byte(in)); got != want {
			t.Errorf("stringifyJSONCell(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestValueOr(t *testing.T) {
	if valueOr("", "def") != "def" {
		t.Error("empty should return default")
	}
	if valueOr("x", "def") != "x" {
		t.Error("non-empty should return value")
	}
}
