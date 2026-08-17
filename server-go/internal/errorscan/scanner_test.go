package errorscan

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTickQueryUsesPositionalPlaceholders is the regression guard for the
// bug that made this whole package a no-op.
//
// The driver is lib/pq (see internal/db), which does NOT translate `?`
// into $N — it forwards the byte to Postgres, which rejects the statement
// at parse time. tick() logged "errorscan: query log lines" at Warn and
// returned on every single tick, so ErrorEvent was never populated and the
// dashboard's error feed stayed permanently empty. Nothing failed loudly
// because the scanner is best-effort by design.
//
// We scan the package source rather than executing the query so the guard
// runs without a live Postgres, which is exactly the condition under which
// the original bug slipped through (this package had zero tests).
func TestTickQueryUsesPositionalPlaceholders(t *testing.T) {
	t.Parallel()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}

	var checked int
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				// Only SQL-looking literals: they name the quoted table.
				if !strings.Contains(lit.Value, `"LogLine"`) &&
					!strings.Contains(strings.ToUpper(lit.Value), "SELECT ") {
					return true
				}
				checked++
				if strings.Contains(lit.Value, "?") {
					t.Errorf("%s: SQL literal uses a `?` placeholder, which lib/pq "+
						"passes through literally and Postgres rejects. Use $1, $2, …:\n%s",
						fset.Position(lit.Pos()), lit.Value)
				}
				return true
			})
		}
	}
	if checked == 0 {
		t.Fatal("no SQL literals found to check — the guard is not actually scanning anything")
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	t.Parallel()

	matches := []string{
		"2026-08-17 12:00:00 ERROR failed to connect",
		"FATAL: database is not accepting connections",
		"panic: runtime error: index out of range [3] with length 2",
		"Traceback (most recent call last):",
		"Exception: invalid literal for int()",
		"java.lang.NullPointerException",
		"UnhandledPromiseRejection: TypeError",
		"Exception in thread \"main\" java.lang.NullPointerException",
		"ActiveRecord::RecordNotFound",
		`{"level":"info","status":500,"path":"/api/x"}`,
	}
	for _, line := range matches {
		if !matchesAnyPattern(line) {
			t.Errorf("matchesAnyPattern(%q) = false, want true", line)
		}
	}

	// Ordinary lines must NOT match — a false positive here floods
	// ErrorEvent and makes the dashboard feed useless.
	nonMatches := []string{
		"GET /healthz 200 1ms",
		"listening on :3000",
		"user signed in successfully",
		`{"level":"info","status":200}`,
		"migration applied: 0009_tenancy_rev",
	}
	for _, line := range nonMatches {
		if matchesAnyPattern(line) {
			t.Errorf("matchesAnyPattern(%q) = true, want false", line)
		}
	}
}

// TestMatchesAnyPattern_KnownGaps documents error lines that a user would
// reasonably expect to be caught but currently are NOT. Found while adding
// the first tests to this package (it previously had none).
//
// The `\bException\b.*?:` pattern requires "Exception" as a STANDALONE
// word, so the most common real-world forms miss:
//
//	RuntimeException: …          → no match (no word boundary before "Exception")
//	NullPointerException: …      → no match (ditto)
//	ValueError: / KeyError: …    → no match (Python's actual top-level errors)
//
// Python tracebacks are still caught by the "Traceback" pattern when the
// traceback header is present, and java.lang.* by its own pattern — so this
// is a partial-coverage gap, not a total blind spot. Widening the regex is a
// product decision (it trades recall against false positives on lines like
// "no exception raised"), so this test records the behaviour rather than
// asserting a fix. Skipped so it never blocks CI.
func TestMatchesAnyPattern_KnownGaps(t *testing.T) {
	t.Skip("documents current known-miss patterns; widening the regex is a product decision")

	for _, line := range []string{
		"RuntimeException: invalid literal",
		"NullPointerException: cannot read property",
		"ValueError: invalid literal for int() with base 10",
		"KeyError: 'user_id'",
	} {
		if !matchesAnyPattern(line) {
			t.Errorf("matchesAnyPattern(%q) = false", line)
		}
	}
}

// TestFingerprintFor_CollapsesNoisyVariants is the property the whole
// aggregation depends on: the same code path hit twice with different
// request IDs / counters must land on ONE fingerprint, or the dashboard
// shows N groups of 1 instead of 1 group of N.
func TestFingerprintFor_CollapsesNoisyVariants(t *testing.T) {
	t.Parallel()

	a := fingerprintFor(`ERROR request 12345 failed for user "alice" id 9f3c1a7ebc含`)
	b := fingerprintFor(`ERROR request 67890 failed for user "bob" id 4a2b8d9efa含`)
	if a != b {
		t.Errorf("noisy variants produced different fingerprints: %q vs %q", a, b)
	}

	// Genuinely different code paths must NOT collapse together.
	c := fingerprintFor("ERROR failed to open socket")
	if a == c {
		t.Errorf("distinct messages collapsed to the same fingerprint %q", a)
	}

	if len(a) != 16 {
		t.Errorf("fingerprint length = %d, want 16 hex chars", len(a))
	}
}

// TestFingerprintFor_LongLineIsStable guards the 256-char truncation: a
// stack trace whose tail differs per occurrence must still fingerprint
// identically, otherwise every panic becomes its own group.
func TestFingerprintFor_LongLineIsStable(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("x", 256)
	if got, want := fingerprintFor(prefix+"tail-one"), fingerprintFor(prefix+"tail-two"); got != want {
		t.Errorf("long lines differing only past 256 chars fingerprinted differently: %q vs %q", got, want)
	}
}

// TestTruncateProducesValidUTF8 guards against byte-slicing a multi-byte
// rune in half. Both truncators cut at a byte offset; a log line with
// non-ASCII text (any non-English app log) can land a cut mid-rune, which
// produces an invalid UTF-8 string. Postgres text columns reject invalid
// UTF-8 outright, so the INSERT would fail and the event would be lost.
func TestTruncateProducesValidUTF8(t *testing.T) {
	t.Parallel()

	// "é" is 2 bytes; 200 of them straddles the 250-byte message cap
	// and the 4096-byte raw cap at a non-boundary offset.
	msg := truncateForMessage(strings.Repeat("é", 200))
	if !utf8.ValidString(msg) {
		t.Errorf("truncateForMessage produced invalid UTF-8 (byte-sliced mid-rune)")
	}

	raw := truncateForRaw(strings.Repeat("é", 4000))
	if !utf8.ValidString(raw) {
		t.Errorf("truncateForRaw produced invalid UTF-8 (byte-sliced mid-rune)")
	}
}

func TestTruncateForMessage_ShortLineUnchanged(t *testing.T) {
	t.Parallel()

	const line = "ERROR something broke"
	if got := truncateForMessage("  " + line + "  "); got != line {
		t.Errorf("truncateForMessage(%q) = %q, want trimmed %q", line, got, line)
	}
}
