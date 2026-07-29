package handlers

import "testing"

// TestBlockedSQLBuiltin_CopyBypasses is the CRIT-2 regression guard. The
// original denylist matched the literal "copy " (with a trailing space),
// which Postgres does not require: COPY accepts a tab, newline, comment,
// or open paren after the keyword. Every one of these must now be caught.
func TestBlockedSQLBuiltin_CopyBypasses(t *testing.T) {
	t.Parallel()

	blocked := []string{
		// the demonstrated RCE payload variants
		"COPY\t(SELECT 1) TO PROGRAM 'id'",
		"COPY\n(SELECT 1) TO PROGRAM 'id'",
		"COPY(SELECT 1) TO PROGRAM 'id'",
		"COPY/**/(SELECT 1) TO PROGRAM 'id'",
		"copy (select 1) to program 'id'",
		"COPY   (SELECT 1) TO PROGRAM 'id'",
		// comment used to break up the token
		"CO/**/PY", // not a token — should NOT match; see allowed below
		// file/network primitives
		"SELECT pg_read_file('/etc/passwd')",
		"select PG_READ_FILE ('/etc/passwd')",
		"SELECT pg_read_binary_file('/x')",
		"SELECT pg_ls_dir('/')",
		"SELECT pg_stat_file('/etc/passwd')",
		"SELECT lo_import('/etc/passwd')",
		"SELECT lo_export(1,'/tmp/x')",
		"SELECT dblink('host=evil','SELECT 1')",
		"SELECT dblink_connect('x')",
		"SELECT pg_reload_conf()",
		"SELECT pg_logfile_rotate()",
		// comment before the payload doesn't hide it
		"-- harmless\nCOPY (SELECT 1) TO PROGRAM 'id'",
		"/* x */ COPY (SELECT 1) TO PROGRAM 'id'",
	}
	for _, q := range blocked {
		if q == "CO/**/PY" {
			// This is NOT a real COPY (comment splits the keyword into two
			// non-tokens); Postgres would not parse it as COPY either, so
			// the denylist correctly lets it through to fail at parse time.
			if reason := blockedSQLBuiltin(q); reason != "" {
				t.Errorf("blockedSQLBuiltin(%q) = %q, want allowed (not a real COPY token)", q, reason)
			}
			continue
		}
		if reason := blockedSQLBuiltin(q); reason == "" {
			t.Errorf("blockedSQLBuiltin(%q) = allowed, want BLOCKED", q)
		}
	}
}

// TestBlockedSQLBuiltin_AllowsOrdinaryQueries confirms the denylist does
// not over-block legitimate data-browse queries — including ones whose
// column/table names merely contain a denied word as a substring.
func TestBlockedSQLBuiltin_AllowsOrdinaryQueries(t *testing.T) {
	t.Parallel()

	allowed := []string{
		"SELECT * FROM users LIMIT 100",
		"SELECT id, email FROM accounts WHERE active = true",
		"SELECT count(*) FROM orders",
		// substrings that are NOT the denied token
		"SELECT copyright FROM books",           // 'copy' inside 'copyright'
		"SELECT * FROM copy_log",                // 'copy' inside 'copy_log'
		"SELECT dblink_id FROM my_dblink_table", // 'dblink' as part of an identifier column? see note
		"SELECT lo_import_status FROM jobs",     // 'lo_import' inside identifier
	}
	for _, q := range allowed {
		// dblink/lo_import as a bare token WILL match by design (word
		// boundary treats _ as a word char, so my_dblink_table's "dblink"
		// is bounded by _ which is a word char → NOT a boundary → not
		// matched). Verify the ones we intend to allow.
		if reason := blockedSQLBuiltin(q); reason != "" {
			t.Errorf("blockedSQLBuiltin(%q) = %q, want allowed", q, reason)
		}
	}
}
