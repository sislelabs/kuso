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
		`"hello"`:       "hello",
		`42`:            "42",
		`3.14`:          "3.14",
		`null`:          "",
		`true`:          "true",
		`["a","b"]`:     `["a","b"]`,
		`{"k":1}`:       `{"k":1}`,
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

func TestBlockedClickHouseClause(t *testing.T) {
	blocked := []string{
		"SELECT 1 SETTINGS max_execution_time=0",
		"SELECT count() FROM system.numbers settings max_result_rows=0",
		"SELECT 1) SETTINGS max_execution_time=0 --",      // the subquery-wrap breakout
		"SELECT 1 /*x*/ SETTINGS max_memory_usage=999999", // comment before doesn't hide it
	}
	for _, q := range blocked {
		if reason := blockedClickHouseClause(q); reason == "" {
			t.Errorf("expected %q to be blocked (SETTINGS), but it passed", q)
		}
	}
	allowed := []string{
		"SELECT count() FROM events",
		"SELECT * FROM events WHERE note = 'my settings are fine'", // 'settings' inside a literal
		"SELECT 'settings' AS x",                                   // literal only
		"SELECT settings_col FROM t",                               // word boundary: settings_col != settings
	}
	for _, q := range allowed {
		if reason := blockedClickHouseClause(q); reason != "" {
			t.Errorf("expected %q to pass, but it was blocked: %s", q, reason)
		}
	}
}
func TestChIdentifier(t *testing.T) {
	cases := map[string]string{
		"events":    "`events`",
		"my table":  "`my table`",
		"has`tick":  "`has``tick`", // backtick doubled
		"litetrack": "`litetrack`",
	}
	for in, want := range cases {
		if got := chIdentifier(in); got != want {
			t.Errorf("chIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChStringLiteral(t *testing.T) {
	cases := map[string]string{
		"events":     "'events'",
		"o'brien":    `'o\'brien'`,
		`back\slash`: `'back\\slash'`,
	}
	for in, want := range cases {
		if got := chStringLiteral(in); got != want {
			t.Errorf("chStringLiteral(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseClickHouseEnum(t *testing.T) {
	got, ok := parseClickHouseEnum("Enum8('a' = 1, 'b' = 2, 'c' = 3)")
	if !ok || len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Errorf("Enum8 parse = %v (ok=%v), want [a b c]", got, ok)
	}
	// wrapped in Nullable
	got2, ok2 := parseClickHouseEnum("Nullable(Enum16('x' = 10, 'y' = 20))")
	if !ok2 || len(got2) != 2 || got2[1] != "y" {
		t.Errorf("Nullable(Enum16) parse = %v (ok=%v), want [x y]", got2, ok2)
	}
	// not an enum
	if _, ok3 := parseClickHouseEnum("LowCardinality(String)"); ok3 {
		t.Error("LowCardinality(String) should not parse as enum")
	}
	if _, ok4 := parseClickHouseEnum("Nullable(String)"); ok4 {
		t.Error("Nullable(String) should not parse as enum")
	}
}

func TestValidClickHouseDir(t *testing.T) {
	if validClickHouseDir("desc") != "DESC" || validClickHouseDir("DESC") != "DESC" {
		t.Error("desc should normalize to DESC")
	}
	if validClickHouseDir("") != "ASC" || validClickHouseDir("asc") != "ASC" || validClickHouseDir("garbage") != "ASC" {
		t.Error("non-desc should default to ASC")
	}
}
