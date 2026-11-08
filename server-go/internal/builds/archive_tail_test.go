package builds

import (
	"strings"
	"testing"
)

func TestCapLogTail(t *testing.T) {
	lines := make([]string, 50)
	for i := range lines {
		lines[i] = strings.Repeat("x", 9) // 10 bytes with the newline
	}
	logs := strings.Join(lines, "\n")

	if got := capLogTail(logs, 100, 1<<20); got != logs {
		t.Fatalf("under both caps the log must be untouched")
	}
	if got := strings.Count(capLogTail(logs, 20, 1<<20), "\n"); got != 19 {
		t.Fatalf("line cap: got %d newlines, want 19", got)
	}
	got := capLogTail(logs, 100, 95)
	if len(got) > 95 {
		t.Fatalf("byte cap: got %d bytes, want <= 95", len(got))
	}
	for _, l := range strings.Split(got, "\n") {
		if len(l) != 9 {
			t.Fatalf("byte cap must drop whole lines, got partial line %q", l)
		}
	}
}
