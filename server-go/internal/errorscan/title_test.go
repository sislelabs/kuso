package errorscan

import "testing"

func TestCanonicalTitle(t *testing.T) {
	cases := []struct {
		title string
		lines []string
		want  string
	}{
		{"2026-11-08T19:40:05Z ERROR failed to send receipt order_id=1012", nil, "failed to send receipt order_id=1012"},
		{"[2026-11-08 19:40:05.123] [error] boom", nil, "boom"},
		{"TypeError: x is undefined", nil, "TypeError: x is undefined"},
		{"ERROR", nil, "ERROR"},
		// A traceback cut before its exception line names what it has.
		{"Traceback (most recent call last):", []string{"Traceback (most recent call last):", `  File "app.py", line 3, in <module>`, "ValueError: bad input"}, "ValueError: bad input"},
		{"Traceback (most recent call last):", []string{"Traceback (most recent call last):", `  File "app.py", line 3`}, "Python traceback (exception line not captured)"},
	}
	for _, c := range cases {
		if got := canonicalTitle(c.title, c.lines); got != c.want {
			t.Errorf("canonicalTitle(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

func TestTitleFromRaw(t *testing.T) {
	raw := "2026-11-08T19:40:05Z ERROR job failed\nTraceback (most recent call last):\n  File \"w.py\", line 9, in run\nKeyError: 'cart'"
	if got := titleFromRaw(raw); got != "KeyError: 'cart'" {
		t.Errorf("titleFromRaw = %q", got)
	}
	bare := "Traceback (most recent call last):\n  File \"w.py\", line 9, in run\nKeyError: 'cart'"
	if got := titleFromRaw(bare); got != "KeyError: 'cart'" {
		t.Errorf("titleFromRaw(bare) = %q", got)
	}
	// Old rows split by timestamp normalize to the same fingerprint.
	a := fingerprintFor(titleFromRaw("2026-11-08T19:40:05Z ERROR db timeout after 250ms"))
	b := fingerprintFor(titleFromRaw("2026-11-08T21:02:11Z ERROR db timeout after 300ms"))
	if a != b {
		t.Error("timestamp variants got different fingerprints")
	}
}
