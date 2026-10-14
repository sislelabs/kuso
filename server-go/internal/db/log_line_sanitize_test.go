package db

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSanitizeLogLine(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"ascii over limit":     strings.Repeat("a", MaxLogLineChars+500),
		"ascii exactly limit":  strings.Repeat("a", MaxLogLineChars),
		"multibyte over limit": strings.Repeat("я", MaxLogLineChars+1),
		// Byte length is 3x the limit but rune count is under it: must
		// be kept whole, not byte-sliced.
		"multibyte under char limit": strings.Repeat("€", MaxLogLineChars-1),
		"invalid utf8":               "ok \xff\xfe bad",
		"nul bytes":                  "a\x00b",
	}
	for name, in := range cases {
		out := SanitizeLogLine(in)
		if !utf8.ValidString(out) {
			t.Errorf("%s: output not valid UTF-8", name)
		}
		if n := utf8.RuneCountInString(out); n > MaxLogLineChars {
			t.Errorf("%s: %d chars, over the %d CHECK", name, n, MaxLogLineChars)
		}
		if strings.ContainsRune(out, 0) {
			t.Errorf("%s: NUL survived", name)
		}
	}
	if in := strings.Repeat("€", MaxLogLineChars-1); SanitizeLogLine(in) != in {
		t.Error("line within the char limit was altered")
	}
	if out := SanitizeLogLine(strings.Repeat("a", MaxLogLineChars+1)); !strings.HasSuffix(out, logLineTruncSuffix) {
		t.Error("truncated line lacks the suffix")
	}
}

// TestInsertLogLines_OversizedAndBadRowsDontPoisonBatch is the DB-2
// regression: a >16KB line used to violate LogLine_line_len_chk and
// fail the whole multi-VALUES insert. PG-backed.
func TestInsertLogLines_OversizedAndBadRowsDontPoisonBatch(t *testing.T) {
	d := openTestDB(t).AsLogDB()
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(line string) LogLine {
		return LogLine{Ts: now, Pod: "p", Project: "a", Service: "s", Line: line}
	}
	batch := []LogLine{
		mk("good-1"),
		mk(strings.Repeat("a", 20000)),
		mk(strings.Repeat("я", 9000)), // 18000 bytes, 9000 chars: fits
		mk(strings.Repeat("я", 17000)),
		mk("bad \xff utf8"),
		mk("good-2"),
	}
	// A row Postgres rejects for a reason sanitising can't fix.
	batch = append(batch, LogLine{Ts: time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC), Pod: "p", Project: "a", Service: "s", Line: "out-of-range"})
	batch = append(batch, mk("good-3"))

	if err := d.InsertLogLines(ctx, batch); err != nil {
		t.Fatalf("InsertLogLines: %v", err)
	}
	var n, over int
	if err := d.QueryRowContext(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE length(line) > $1) FROM "LogLine"`, MaxLogLineChars).Scan(&n, &over); err != nil {
		t.Fatal(err)
	}
	if n != len(batch)-1 || over != 0 {
		t.Errorf("stored %d rows (%d over limit), want %d rows, 0 over", n, over, len(batch)-1)
	}
}
